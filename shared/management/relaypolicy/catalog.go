package relaypolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const (
	// MaxCatalogEntries bounds both operator publication and decoded policy identities.
	MaxCatalogEntries = 4096
	// MaxRelayAliases bounds approved alternate endpoints for one concrete instance.
	MaxRelayAliases = 128
	// MaxRelayEndpointLength bounds native relay addresses before URL parsing.
	MaxRelayEndpointLength = 512
	// MaxRelayIDLength bounds stable operator relay identities.
	MaxRelayIDLength = 128
)

// CanonicalEndpoint validates and normalizes a native relay URL without resolving DNS.
func CanonicalEndpoint(endpoint RelayEndpoint) (RelayEndpoint, error) {
	if len(endpoint) > MaxRelayEndpointLength {
		return "", fmt.Errorf("relay endpoint exceeds length limit")
	}
	u, err := url.Parse(string(endpoint))
	if err != nil {
		return "", fmt.Errorf("parse relay endpoint: %w", err)
	}
	if (u.Scheme != "rel" && u.Scheme != "rels") || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" || u.Path != "" || u.RawQuery != "" ||
		u.ForceQuery || u.Fragment != "" {
		return "", fmt.Errorf("invalid native relay endpoint")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if addr, parseErr := netip.ParseAddr(host); parseErr == nil {
		if addr.Zone() != "" {
			return "", fmt.Errorf("scoped relay addresses are not supported")
		}
		host = addr.Unmap().String()
	} else if !validHostname(host) {
		return "", fmt.Errorf("invalid relay hostname")
	}
	port := u.Port()
	if port != "" {
		n, parseErr := strconv.ParseUint(port, 10, 16)
		if parseErr != nil || n == 0 {
			return "", fmt.Errorf("invalid relay port")
		}
		u.Host = net.JoinHostPort(host, strconv.FormatUint(n, 10))
	} else {
		if strings.HasSuffix(u.Host, ":") {
			return "", fmt.Errorf("empty relay port")
		}
		u.Host = host
		if strings.Contains(host, ":") {
			u.Host = "[" + host + "]"
		}
	}
	return RelayEndpoint(u.String()), nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

// ValidateCatalog returns a canonical, detached catalog or rejects ambiguous identities.
func ValidateCatalog(catalog RelayCatalog) (RelayCatalog, error) {
	result := RelayCatalog{Generation: catalog.Generation}
	if len(catalog.Entries) > MaxCatalogEntries {
		return result, fmt.Errorf("relay catalog exceeds entry limit")
	}
	ids := make(map[RelayID]struct{}, len(catalog.Entries))
	endpoints := make(map[RelayEndpoint]RelayID)
	for _, entry := range catalog.Entries {
		if entry.ID == "" || len(entry.ID) > MaxRelayIDLength || strings.TrimSpace(string(entry.ID)) != string(entry.ID) {
			return result, fmt.Errorf("empty or invalid relay identity")
		}
		if _, exists := ids[entry.ID]; exists {
			return result, fmt.Errorf("duplicate relay identity %s", entry.ID)
		}
		ids[entry.ID] = struct{}{}
		if len(entry.Aliases) > MaxRelayAliases {
			return result, fmt.Errorf("relay %s exceeds alias limit", entry.ID)
		}
		var err error
		entry.Endpoint, err = CanonicalEndpoint(entry.Endpoint)
		if err != nil {
			return result, fmt.Errorf("relay %s endpoint: %w", entry.ID, err)
		}
		if entry.InstanceEndpoint == "" {
			entry.InstanceEndpoint = entry.Endpoint
		}
		entry.InstanceEndpoint, err = CanonicalEndpoint(entry.InstanceEndpoint)
		if err != nil {
			return result, fmt.Errorf("relay %s instance: %w", entry.ID, err)
		}
		entry.Aliases = slices.Clone(entry.Aliases)
		for i := range entry.Aliases {
			entry.Aliases[i], err = CanonicalEndpoint(entry.Aliases[i])
			if err != nil {
				return result, fmt.Errorf("relay %s alias: %w", entry.ID, err)
			}
		}
		addresses := append(slices.Clone(entry.Aliases), entry.Endpoint, entry.InstanceEndpoint)
		for _, address := range addresses {
			if owner, exists := endpoints[address]; exists && owner != entry.ID {
				return result, fmt.Errorf("ambiguous relay endpoint for %s and %s", owner, entry.ID)
			}
			endpoints[address] = entry.ID
		}
		slices.Sort(entry.Aliases)
		entry.Aliases = slices.Compact(entry.Aliases)
		result.Entries = append(result.Entries, entry)
	}
	slices.SortFunc(result.Entries, func(a, b RelayCatalogEntry) int {
		return strings.Compare(string(a.ID), string(b.ID))
	})
	return result, nil
}

// BootstrapCatalog preserves legacy addresses using order-independent, non-policy identities.
// Explicit entries are required before an account can reference a relay in a policy.
func BootstrapCatalog(addresses []string, explicit []RelayCatalogEntry) (RelayCatalog, error) {
	catalog, err := ValidateCatalog(RelayCatalog{Entries: explicit})
	if err != nil {
		return catalog, err
	}
	known := make(map[RelayEndpoint]struct{})
	for _, entry := range catalog.Entries {
		known[entry.Endpoint] = struct{}{}
		known[entry.InstanceEndpoint] = struct{}{}
		for _, alias := range entry.Aliases {
			known[alias] = struct{}{}
		}
	}
	for _, address := range addresses {
		endpoint, err := CanonicalEndpoint(RelayEndpoint(address))
		if err != nil {
			// Legacy advertisement remains untouched; only managed catalog entries require canonical URLs.
			continue
		}
		if _, exists := known[endpoint]; exists {
			continue
		}
		known[endpoint] = struct{}{}
		hash := sha256.Sum256([]byte(endpoint))
		catalog.Entries = append(catalog.Entries, RelayCatalogEntry{
			ID: RelayID("legacy-" + hex.EncodeToString(hash[:])), Endpoint: endpoint,
			InstanceEndpoint: endpoint, Active: true, Legacy: true,
		})
	}
	return ValidateCatalog(catalog)
}

// ValidateInstance rejects relay handshakes not mapped to the chosen concrete instance.
func ValidateInstance(entry RelayCatalogEntry, endpoint RelayEndpoint) error {
	canonical, err := CanonicalEndpoint(endpoint)
	if err != nil {
		return err
	}
	catalog, err := ValidateCatalog(RelayCatalog{Entries: []RelayCatalogEntry{entry}})
	if err != nil {
		return err
	}
	approved := catalog.Entries[0]
	if canonical == approved.InstanceEndpoint || slices.Contains(approved.Aliases, canonical) {
		return nil
	}
	return fmt.Errorf("relay %s returned an unapproved instance", entry.ID)
}
