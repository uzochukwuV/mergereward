// SPDX-License-Identifier: MIT
pragma solidity 0.8.26;

/**
 * MergeReward — GitHub-Native Developer Funding via CRE
 *
 * Flow:
 *  1. Maintainer calls `createBounty(repoId, issueNumber)` + sends ETH
 *  2. Developer submits a PR, gets merged on GitHub
 *  3. CRE workflow detects the merge, calls `releaseBounty` via KeystoneForwarder
 *  4. Developer receives ETH automatically — no manual payout
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

    // ─── Errors ───────────────────────────────────────────────────────────────

    error BountyAlreadyExists();
    error BountyNotFound();
    error BountyAlreadySettled();
    error Unauthorized();
    error NotExpiredYet();
    error AlreadyClaimed();
    error NoClaimer();
    error ZeroAmount();

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
        if (b.maintainer == address(0))   revert BountyNotFound();
        if (b.released || b.refunded)     revert BountyAlreadySettled();
        if (msg.sender != b.maintainer)   revert Unauthorized();
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
     * @notice Release bounty to the developer. ONLY callable by the CRE
     *         KeystoneForwarder after verifying the GitHub PR was merged.
     *
     * @dev The CRE workflow passes the bountyId, developer address, and
     *      a report signed by the DON. The forwarder verifies the report
     *      and calls this function.
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

        (bool ok1,) = developer.call{value: payout}("");
        require(ok1, "payout failed");

        (bool ok2,) = feeRecipient.call{value: fee}("");
        require(ok2, "fee transfer failed");

        emit BountyReleased(bountyId, developer, payout, fee);
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

    // ─── Internals ────────────────────────────────────────────────────────────

    function _bountyId(string calldata repoId, uint256 issueNumber)
        internal pure returns (bytes32)
    {
        return keccak256(abi.encodePacked(repoId, issueNumber));
    }

    // ─── Receive ETH ──────────────────────────────────────────────────────────
    receive() external payable {}
}
