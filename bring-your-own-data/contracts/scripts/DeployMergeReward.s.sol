// SPDX-License-Identifier: MIT
pragma solidity ^0.8.26;

import {Script, console} from "forge-std/Script.sol";
import {MergeReward} from "../src/MergeReward.sol";

/**
 * Deploy MergeReward to Sepolia or Base Sepolia.
 *
 * Usage:
 *   forge script scripts/DeployMergeReward.s.sol \
 *     --rpc-url $RPC_URL \
 *     --broadcast \
 *     --verify \
 *     -vvvv
 *
 * Required env vars:
 *   PRIVATE_KEY      — deployer private key
 *   FEE_RECIPIENT    — address that receives the 5% protocol fee
 */
contract DeployMergeReward is Script {
    function run() external {
        address feeRecipient = vm.envAddress("FEE_RECIPIENT");
        uint256 deployerKey  = vm.envUint("PRIVATE_KEY");

        vm.startBroadcast(deployerKey);

        MergeReward mr = new MergeReward(
            "merge-reward",        // Must match workflow-name in workflow.yaml
            payable(feeRecipient)
        );

        console.log("MergeReward deployed at:", address(mr));
        console.log("KeystoneForwarder expected:", mr.keystoneForwarder());
        console.log("WorkflowName (bytes10):", uint80(uint(bytes32(mr.workflowName()))));

        vm.stopBroadcast();
    }
}
