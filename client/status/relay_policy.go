package status

import (
	"fmt"
	"strings"

	"github.com/netbirdio/netbird/client/proto"
)

// RelayPolicyOutput exposes credential-free policy intent and the observed path.
type RelayPolicyOutput struct {
	RemotePeerKey         string `json:"remotePeerKey" yaml:"remotePeerKey"`
	DesiredRevision       uint64 `json:"desiredRevision" yaml:"desiredRevision"`
	AppliedRevision       uint64 `json:"appliedRevision" yaml:"appliedRevision"`
	Digest                string `json:"digest" yaml:"digest"`
	CatalogGeneration     uint64 `json:"catalogGeneration" yaml:"catalogGeneration"`
	ReselectionGeneration uint64 `json:"reselectionGeneration" yaml:"reselectionGeneration"`
	Attempt               uint64 `json:"attempt" yaml:"attempt"`
	Transport             string `json:"transport" yaml:"transport"`
	RelayID               string `json:"relayId,omitempty" yaml:"relayId,omitempty"`
	State                 string `json:"state" yaml:"state"`
	Reason                string `json:"reason,omitempty" yaml:"reason,omitempty"`
}

func mapRelayPolicy(p *proto.RelayPolicyStatus) *RelayPolicyOutput {
	if p == nil {
		return nil
	}
	return &RelayPolicyOutput{RemotePeerKey: p.GetRemotePeerKey(), DesiredRevision: p.GetDesiredRevision(), AppliedRevision: p.GetAppliedRevision(), Digest: p.GetDigest(), CatalogGeneration: p.GetCatalogGeneration(), ReselectionGeneration: p.GetReselectionGeneration(), Attempt: p.GetAttempt(), Transport: p.GetTransport(), RelayID: p.GetRelayId(), State: p.GetState(), Reason: p.GetReason()}
}

func mapRelayPolicies(policies []*proto.RelayPolicyStatus) []RelayPolicyOutput {
	output := make([]RelayPolicyOutput, 0, len(policies))
	for _, p := range policies {
		if mapped := mapRelayPolicy(p); mapped != nil {
			output = append(output, *mapped)
		}
	}
	return output
}

func relayPolicySummary(policies []RelayPolicyOutput) string {
	if len(policies) == 0 {
		return ""
	}
	var text strings.Builder
	text.WriteString("Relay policies:\n")
	for _, p := range policies {
		scope := "Home registration"
		if p.RemotePeerKey != "" {
			scope = "Pair " + p.RemotePeerKey
		}
		fmt.Fprintf(&text, "  %s: %s, desired/applied revision %d/%d\n", scope, p.State, p.DesiredRevision, p.AppliedRevision)
		fmt.Fprintf(&text, "    Transport: %s; approved relay: %s; reason: %s\n", p.Transport, p.RelayID, p.Reason)
		fmt.Fprintf(&text, "    Digest: %s; catalog/reselection: %d/%d; attempt: %d\n", p.Digest, p.CatalogGeneration, p.ReselectionGeneration, p.Attempt)
	}
	return text.String()
}
