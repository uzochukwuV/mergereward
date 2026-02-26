package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/smartcontractkit/cre-sdk-go/capabilities/blockchain/evm"
	"github.com/smartcontractkit/cre-sdk-go/capabilities/networking/http"
	"github.com/smartcontractkit/cre-sdk-go/cre"
)

// ─── Config ──────────────────────────────────────────────────────────────────
// Loaded from config.json (non-secret values)

type EVMConfig struct {
	MergeRewardAddress string `json:"mergeRewardAddress"` // deployed MergeReward.sol address
	ChainName          string `json:"chainName"`
	GasLimit           uint64 `json:"gasLimit"`
}

func (e *EVMConfig) NewEVMClient() (*evm.Client, error) {
	sel, err := evm.ChainSelectorFromName(e.ChainName)
	if err != nil {
		return nil, err
	}
	return &evm.Client{ChainSelector: sel}, nil
}

type Config struct {
	// Your backend API that exposes merged PR data
	// e.g. https://api.mergereward.xyz/merged-prs
	BackendURL string      `json:"backendURL"`
	EVMs       []EVMConfig `json:"evms"`
}

// ─── API Types ───────────────────────────────────────────────────────────────
// Shape of what your backend returns for a merged PR event

type MergedPREvent struct {
	RepoID      string `json:"repoId"`      // "owner/repo"
	IssueNumber uint64 `json:"issueNumber"` // GitHub issue number
	PRNumber    uint64 `json:"prNumber"`    // GitHub PR number
	DeveloperAddress string `json:"developerAddress"` // EVM address of the developer
}

// Consensus-aggregated: all DON nodes must agree on the same merge event
type PRMergeInfo struct {
	BountyID         [32]byte `consensus_aggregation:"mode" json:"bountyId"`
	DeveloperAddress string   `consensus_aggregation:"mode" json:"developerAddress"`
}

// ─── ABI encoding helper ─────────────────────────────────────────────────────
// Matches releaseBounty(bytes32 bountyId, address developer) in MergeReward.sol

func encodeReleaseBountyCall(bountyID [32]byte, developer common.Address) ([]byte, error) {
	// Function selector: keccak256("releaseBounty(bytes32,address)")[:4]
	sig := []byte("releaseBounty(bytes32,address)")
	selector := crypto.Keccak256(sig)[:4]

	// ABI-encode arguments: bytes32 is left-aligned, address is right-aligned in 32 bytes
	args := make([]byte, 64)
	copy(args[0:32], bountyID[:])
	copy(args[44:64], developer.Bytes()) // address is 20 bytes, right-aligned in slot

	return append(selector, args...), nil
}

// ─── Workflow ─────────────────────────────────────────────────────────────────

// InitWorkflow is called once by the CRE SDK to register triggers and handlers.
// We use an HTTP trigger driven by your backend — the backend calls the CRE
// webhook endpoint whenever a PR merges on GitHub (via GitHub Webhooks).
func InitWorkflow(config *Config, logger *slog.Logger, secretsProvider cre.SecretsProvider) (cre.Workflow[*Config], error) {
	workflow := cre.Workflow[*Config]{
		// Trigger: Your backend POSTs to this workflow's HTTP endpoint
		// when it receives a `pull_request` webhook from GitHub with action=closed+merged=true
		cre.Handler(
			http.Trigger(&http.TriggerConfig{
				// CRE generates this endpoint; register it as your GitHub webhook target
				// via your backend or directly in GitHub repo settings.
			}),
			onPRMergedWebhook,
		),
	}
	return workflow, nil
}

// onPRMergedWebhook fires when your backend forwards a GitHub PR merge event.
func onPRMergedWebhook(config *Config, runtime cre.Runtime, payload *http.TriggerPayload) (string, error) {
	logger := runtime.Logger()

	// 1. Parse the incoming webhook body
	var event MergedPREvent
	if err := json.Unmarshal(payload.Body, &event); err != nil {
		return "", fmt.Errorf("failed to parse PR merge event: %w", err)
	}
	logger.Info("PR merge event received",
		"repo", event.RepoID,
		"issue", event.IssueNumber,
		"pr", event.PRNumber,
		"developer", event.DeveloperAddress,
	)

	// 2. Confidential HTTP: verify the merge against the GitHub API
	//    Your GitHub token is stored as a CRE secret — never exposed on-chain
	verifiedInfo, err := verifyPRMergeWithGitHub(config, runtime, &event)
	if err != nil {
		return "", fmt.Errorf("GitHub verification failed: %w", err)
	}

	logger.Info("GitHub verification passed",
		"bountyId", fmt.Sprintf("0x%x", verifiedInfo.BountyID),
		"developer", verifiedInfo.DeveloperAddress,
	)

	// 3. For each target chain, release the bounty on-chain
	for _, evmCfg := range config.EVMs {
		if err := releaseBountyOnChain(evmCfg, runtime, verifiedInfo); err != nil {
			logger.Error("Failed to release bounty on chain",
				"chain", evmCfg.ChainName, "err", err)
			// Continue to other chains — partial success is better than full abort
			continue
		}
		logger.Info("Bounty released", "chain", evmCfg.ChainName)
	}

	return fmt.Sprintf("bounty released for issue %s#%d", event.RepoID, event.IssueNumber), nil
}

// ─── GitHub Verification (Confidential HTTP) ─────────────────────────────────

type GitHubPR struct {
	Merged bool `json:"merged"`
	Number int  `json:"number"`
	Body   string `json:"body"`
}

// verifyPRMergeWithGitHub calls the GitHub REST API to confirm:
//   - The PR is actually merged (not just closed)
//   - It references the expected issue number
//   - Returns the on-chain bountyId and verified developer address
//
// The GitHub token is injected from CRE secrets — the DON nodes hold it
// inside a Trusted Execution Environment, so it never hits the chain.
func verifyPRMergeWithGitHub(
	config *Config,
	runtime cre.Runtime,
	event *MergedPREvent,
) (*PRMergeInfo, error) {

	url := fmt.Sprintf(
		"https://api.github.com/repos/%s/pulls/%d",
		event.RepoID, event.PRNumber,
	)

	result, err := http.SendRequest(
		config,
		runtime,
		&http.Client{},
		func(cfg *Config, logger *slog.Logger, req *http.SendRequester) (*PRMergeInfo, error) {
			resp, err := req.SendRequest(&http.Request{
				Method: "GET",
				Url:    url,
				Headers: map[string]string{
					// SECRET: injected by CRE from your secrets.yaml
					// GitHub token never touches the chain ✓
					"Authorization": fmt.Sprintf("Bearer %s", mustSecret(runtime, "GITHUB_TOKEN")),
					"Accept":        "application/vnd.github+json",
					"X-GitHub-Api-Version": "2022-11-28",
				},
				Timeout:       &durationpb.Duration{Seconds: 10},
				CacheSettings: &http.CacheSettings{},
			}).Await()
			if err != nil {
				return nil, fmt.Errorf("GitHub API request failed: %w", err)
			}

			var pr GitHubPR
			if err := json.Unmarshal(resp.Body, &pr); err != nil {
				return nil, fmt.Errorf("failed to parse GitHub response: %w", err)
			}
			if !pr.Merged {
				return nil, fmt.Errorf("PR #%d is not merged", event.PRNumber)
			}

			// Compute the bountyId the same way as the Solidity contract:
			// keccak256(abi.encodePacked(repoId, issueNumber))
			bountyID := computeBountyID(event.RepoID, event.IssueNumber)

			return &PRMergeInfo{
				BountyID:         bountyID,
				DeveloperAddress: event.DeveloperAddress,
			}, nil
		},
		// DON consensus: all nodes must agree on the same bountyId + developer
		cre.ConsensusAggregationFromTags[*PRMergeInfo](),
	).Await()

	if err != nil {
		return nil, err
	}
	return result, nil
}

// ─── On-Chain Release ─────────────────────────────────────────────────────────

func releaseBountyOnChain(evmCfg EVMConfig, runtime cre.Runtime, info *PRMergeInfo) error {
	logger := runtime.Logger()

	evmClient, err := evmCfg.NewEVMClient()
	if err != nil {
		return fmt.Errorf("failed to create EVM client: %w", err)
	}

	developer := common.HexToAddress(info.DeveloperAddress)
	calldata, err := encodeReleaseBountyCall(info.BountyID, developer)
	if err != nil {
		return fmt.Errorf("failed to encode calldata: %w", err)
	}

	// Generate a CRE report that the KeystoneForwarder will verify on-chain
	reportPromise := runtime.GenerateReport(&cre.ReportRequest{
		EncodedPayload: calldata,
		EncoderName:    "evm",
		SigningAlgo:    "ecdsa",
		HashingAlgo:    "keccak256",
	})
	report, err := reportPromise.Await()
	if err != nil {
		return fmt.Errorf("failed to generate report: %w", err)
	}

	logger.Info("Submitting releaseBounty transaction",
		"contract", evmCfg.MergeRewardAddress,
		"developer", info.DeveloperAddress,
		"chain", evmCfg.ChainName,
	)

	writePromise := evmClient.WriteReport(runtime, &evm.WriteCreReportRequest{
		Receiver:  common.HexToAddress(evmCfg.MergeRewardAddress).Bytes(),
		Report:    report,
		GasConfig: &evm.GasConfig{GasLimit: evmCfg.GasLimit},
	})

	resp, err := writePromise.Await()
	if err != nil {
		return fmt.Errorf("failed to submit transaction: %w", err)
	}
	if resp.TxStatus != evm.TxStatus_TX_STATUS_SUCCESS {
		errMsg := "unknown error"
		if resp.ErrorMessage != nil {
			errMsg = *resp.ErrorMessage
		}
		return fmt.Errorf("transaction failed: %s", errMsg)
	}

	logger.Info("releaseBounty tx confirmed",
		"txHash", common.BytesToHash(resp.TxHash).Hex(),
	)
	return nil
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

// computeBountyID replicates the Solidity:
//
//	keccak256(abi.encodePacked(repoId, issueNumber))
//
// issueNumber is encoded as a uint256 (big-endian, 32 bytes)
func computeBountyID(repoID string, issueNumber uint64) [32]byte {
	repoBytes := []byte(repoID)
	issueBytes := make([]byte, 32)
	big.NewInt(int64(issueNumber)).FillBytes(issueBytes)

	packed := append(repoBytes, issueBytes...)
	hash := crypto.Keccak256(packed)

	var result [32]byte
	copy(result[:], hash)
	return result
}

// mustSecret retrieves a secret from CRE's secrets provider.
// Secrets are stored encrypted and are only available inside the TEE.
func mustSecret(runtime cre.Runtime, key string) string {
	val, err := runtime.SecretsProvider().Get(key)
	if err != nil {
		panic(fmt.Sprintf("missing required secret: %s", key))
	}
	return val
}
