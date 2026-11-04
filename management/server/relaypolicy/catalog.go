package relaypolicy

import shared "github.com/netbirdio/netbird/shared/management/relaypolicy"

// CanonicalEndpoint validates and normalizes a native relay URL without resolving DNS.
func CanonicalEndpoint(endpoint shared.RelayEndpoint) (shared.RelayEndpoint, error) {
	return shared.CanonicalEndpoint(endpoint)
}

// ValidateCatalog returns a canonical, detached catalog or rejects ambiguous identities.
func ValidateCatalog(catalog shared.RelayCatalog) (shared.RelayCatalog, error) {
	return shared.ValidateCatalog(catalog)
}

// BootstrapCatalog preserves legacy advertisement while requiring explicit managed identities.
func BootstrapCatalog(addresses []string, entries []shared.RelayCatalogEntry) (shared.RelayCatalog, error) {
	return shared.BootstrapCatalog(addresses, entries)
}

// ValidateInstance rejects handshakes outside an approved concrete relay identity.
func ValidateInstance(entry shared.RelayCatalogEntry, endpoint shared.RelayEndpoint) error {
	return shared.ValidateInstance(entry, endpoint)
}
