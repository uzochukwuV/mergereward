// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

/**
 * MergeReward — GitHub-Native Developer Funding via CRE
 *
 * Flow:
 *  1. Maintainer calls `createBounty(repoId, issueNumber)` + sends ETH
 *  2. Developer submits a PR, gets merged on GitHub
 *  3. CRE workflow detects the merge, calls `releaseBounty` via KeystoneForwarder
 *  4. Earned ETH is credited to the developer's in-contract balance (not sent immediately)
 *  5. Developer calls `withdraw()` whenever they want — accumulate first, pay gas once
 *
 * ⚠️  DEMO CONTRACT — NOT AUDITED — DO NOT USE IN PRODUCTION
 */

import {Workflow} from "./util/Workflow.sol";

contract MergeReward {
    // ─── Structs ─────────────────────────────────────────────────────────────

    struct Bounty {
        address maintainer;       // who funded the bounty
        uint256 amount;           // ETH locked in escrow
        address payable claimer;  // developer who claimed the issue
        bool    released;         // true once paid out
        bool    refunded;         // true once refunded to maintainer
        uint256 expiresAt;        // unix timestamp — maintainer can refund after this
    }

    // ─── State ────────────────────────────────────────────────────────────────

    /// bountyId = keccak256(repoId, issueNumber)
    mapping(bytes32 => Bounty) public bounties;

    /// Accumulated earnings per developer address — withdraw at any time
    mapping(address => uint256) public developerBalances;

    /// Only the CRE KeystoneForwarder may call `releaseBounty`
    address public immutable keystoneForwarder;

    /// Workflow name registered in CRE (hashed + truncated to bytes10)
    bytes10 public immutable workflowName;

    /// Protocol fee in basis points (500 = 5%)
    uint256 public constant FEE_BPS = 500;
    address payable public immutable feeRecipient;

    // ─── Events ───────────────────────────────────────────────────────────────

    event BountyCreated(
        bytes32 indexed bountyId,
        string  repoId,
        uint256 issueNumber,
        address indexed maintainer,
        uint256 amount,
        uint256 expiresAt
    );

    event IssueClaimed(
        bytes32 indexed bountyId,
        address indexed claimer
    );

    event BountyReleased(
        bytes32 indexed bountyId,
        address indexed developer,
        uint256 payout,
        uint256 fee
    );

    event BountyRefunded(
        bytes32 indexed bountyId,
        address indexed maintainer,
        uint256 amount
    );

    event BountyCancelled(
        bytes32 indexed bountyId,
        address indexed maintainer,
        uint256 amount
    );

    /// Emitted when a developer's in-contract balance grows after a bounty release
    event DeveloperFunded(
        address indexed developer,
        bytes32 indexed bountyId,
        uint256 amount,
        uint256 newBalance
    );

    /// Emitted when a developer withdraws their accumulated balance
    event DeveloperWithdrew(
        address indexed developer,
        address indexed recipient,
        uint256 amount
    );

    // ─── Errors ───────────────────────────────────────────────────────────────

    error BountyAlreadyExists();
    error BountyNotFound();
    error BountyAlreadySettled();
    error Unauthorized();
    error NotExpiredYet();
    error AlreadyClaimed();
    error NoClaimer();
    error ZeroAmount();
    error NothingToWithdraw();
    error WithdrawFailed();

    // ─── Constructor ──────────────────────────────────────────────────────────

    constructor(
        string memory _workflowName,
        address payable _feeRecipient
    ) {
        keystoneForwarder = Workflow.keystoneForwarder(block.chainid);
        workflowName      = Workflow.toOnChainWorkflowName(_workflowName);
        feeRecipient      = _feeRecipient;
    }

    // ─── Maintainer Actions ───────────────────────────────────────────────────

    /**
     * @notice Create a bounty for a GitHub issue. Send ETH as msg.value.
     * @param repoId       GitHub repo identifier, e.g. "owner/repo"
     * @param issueNumber  GitHub issue number
     * @param ttlSeconds   How long before the maintainer can refund (e.g. 30 days = 2592000)
     */
    function createBounty(
        string calldata repoId,
        uint256 issueNumber,
        uint256 ttlSeconds
    ) external payable {
        if (msg.value == 0) revert ZeroAmount();

        bytes32 id = _bountyId(repoId, issueNumber);
        if (bounties[id].maintainer != address(0)) revert BountyAlreadyExists();

        uint256 expiry = block.timestamp + ttlSeconds;
        bounties[id] = Bounty({
            maintainer: msg.sender,
            amount:     msg.value,
            claimer:    payable(address(0)),
            released:   false,
            refunded:   false,
            expiresAt:  expiry
        });

        emit BountyCreated(id, repoId, issueNumber, msg.sender, msg.value, expiry);
    }

    /**
     * @notice Developer calls this to mark themselves as the issue claimer.
     *         Only one claimer per bounty (first come, first served).
     *         In practice your backend can enforce off-chain assignment rules too.
     */
    function claimIssue(string calldata repoId, uint256 issueNumber) external {
        bytes32 id = _bountyId(repoId, issueNumber);
        Bounty storage b = bounties[id];
        if (b.maintainer == address(0))       revert BountyNotFound();
        if (b.released || b.refunded)         revert BountyAlreadySettled();
        if (b.claimer != address(0))          revert AlreadyClaimed();

        b.claimer = payable(msg.sender);
        emit IssueClaimed(id, msg.sender);
    }

    /**
     * @notice Cancel a bounty that has NO claimer yet. Maintainer gets full refund.
     */
    function cancelBounty(string calldata repoId, uint256 issueNumber) external {
        bytes32 id = _bountyId(repoId, issueNumber);
        Bounty storage b = bounties[id];
        if (b.maintainer == address(0))   revert BountyNotFound();
        if (b.released || b.refunded)     revert BountyAlreadySettled();
        if (b.claimer != address(0))      revert AlreadyClaimed(); // can't cancel once claimed
        if (msg.sender != b.maintainer)   revert Unauthorized();

        uint256 amt = b.amount;
        b.amount    = 0;
        b.refunded  = true;

        (bool ok,) = payable(b.maintainer).call{value: amt}("");
        require(ok, "refund failed");

        emit BountyCancelled(id, b.maintainer, amt);
    }

    /**
     * @notice Refund the maintainer if the bounty has expired without a merge.
     */
    function refundExpiredBounty(string calldata repoId, uint256 issueNumber) external {
        bytes32 id = _bountyId(repoId, issueNumber);
        Bounty storage b = bounties[id];
        if (b.maintainer == address(0))    revert BountyNotFound();
        if (b.released || b.refunded)      revert BountyAlreadySettled();
        if (msg.sender != b.maintainer)    revert Unauthorized();
        if (block.timestamp < b.expiresAt) revert NotExpiredYet();

        uint256 amt = b.amount;
        b.amount    = 0;
        b.refunded  = true;

        (bool ok,) = payable(b.maintainer).call{value: amt}("");
        require(ok, "refund failed");

        emit BountyRefunded(id, b.maintainer, amt);
    }

    // ─── CRE-Only Release ─────────────────────────────────────────────────────

    /**
     * @notice Release bounty to the developer's in-contract balance.
     *         ONLY callable by the CRE KeystoneForwarder after verifying the
     *         GitHub PR was merged.
     *
     * @dev Instead of sending ETH immediately, earnings are accumulated in
     *      `developerBalances[developer]`. The developer calls `withdraw()` when
     *      ready — once, for any accumulated amount — avoiding repeated small
     *      transfers and high per-transaction fees.
     *
     * @param bountyId   keccak256(repoId, issueNumber) — matches what the workflow computed
     * @param developer  Address of the developer who opened the merged PR
     */
    function releaseBounty(bytes32 bountyId, address payable developer) external {
        // Only the Keystone forwarder may trigger payouts
        if (msg.sender != keystoneForwarder) revert Unauthorized();

        Bounty storage b = bounties[bountyId];
        if (b.maintainer == address(0)) revert BountyNotFound();
        if (b.released || b.refunded)   revert BountyAlreadySettled();

        // Accept CRE-verified developer even if claimer wasn't set on-chain
        // (supports off-chain claim tracking or direct PR-to-payment flow)
        if (b.claimer != address(0) && b.claimer != developer) revert Unauthorized();

        uint256 total   = b.amount;
        uint256 fee     = (total * FEE_BPS) / 10_000;
        uint256 payout  = total - fee;

        b.amount   = 0;
        b.released = true;
        b.claimer  = developer; // normalise in case it was unset

        // Credit earnings to the developer's in-contract balance.
        // Developer calls withdraw() when they choose — one tx, one fee.
        developerBalances[developer] += payout;

        // Fee goes directly to the protocol fee recipient
        (bool ok,) = feeRecipient.call{value: fee}("");
        require(ok, "fee transfer failed");

        emit BountyReleased(bountyId, developer, payout, fee);
        emit DeveloperFunded(developer, bountyId, payout, developerBalances[developer]);
    }

    // ─── Developer Withdrawal ─────────────────────────────────────────────────

    /**
     * @notice Withdraw all accumulated earnings to the caller's address.
     *         Call this once you've earned enough to make the gas cost worthwhile.
     */
    function withdraw() external {
        _withdraw(payable(msg.sender));
    }

    /**
     * @notice Withdraw all accumulated earnings to a specific recipient address.
     *         Useful for sending directly to a cold wallet or exchange deposit.
     * @param recipient  Address that will receive the ETH
     */
    function withdrawTo(address payable recipient) external {
        if (recipient == address(0)) revert Unauthorized();
        _withdraw(recipient);
    }

    function _withdraw(address payable recipient) internal {
        uint256 amount = developerBalances[msg.sender];
        if (amount == 0) revert NothingToWithdraw();

        // Zero out before transfer (reentrancy protection)
        developerBalances[msg.sender] = 0;

        (bool ok,) = recipient.call{value: amount}("");
        if (!ok) revert WithdrawFailed();

        emit DeveloperWithdrew(msg.sender, recipient, amount);
    }

    // ─── Views ────────────────────────────────────────────────────────────────

    function getBounty(string calldata repoId, uint256 issueNumber)
        external view returns (Bounty memory)
    {
        return bounties[_bountyId(repoId, issueNumber)];
    }

    function getBountyById(bytes32 bountyId)
        external view returns (Bounty memory)
    {
        return bounties[bountyId];
    }

    /**
     * @notice Returns the accumulated (not-yet-withdrawn) earnings for a developer.
     * @param developer  The developer's wallet address
     */
    function getBalance(address developer) external view returns (uint256) {
        return developerBalances[developer];
    }

    // ─── Internals ────────────────────────────────────────────────────────────

    function _bountyId(string calldata repoId, uint256 issueNumber)
        internal pure returns (bytes32)
    {
        return keccak256(abi.encodePacked(repoId, issueNumber));
    }

    // ─── Receive ETH ──────────────────────────────────────────────────────────
    receive() external payable {}
}
