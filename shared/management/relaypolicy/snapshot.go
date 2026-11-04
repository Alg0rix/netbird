package relaypolicy

import "slices"

// RelayAccountState provides optimistic concurrency independently of the network serial.
type RelayAccountState struct {
	AccountID AccountID `gorm:"primaryKey"`
	Revision  uint64
}

// RelayReselection persists a targeted, monotonic home or unordered-pair retry directive.
// PeerB is empty for home registration, otherwise PeerA sorts before PeerB.
type RelayReselection struct {
	AccountID  AccountID `gorm:"primaryKey"`
	PeerA      PeerID    `gorm:"primaryKey;autoIncrement:false"`
	PeerB      PeerID    `gorm:"primaryKey;autoIncrement:false"`
	Generation uint64
}

// Configuration contains account-owned configuration, without catalog publication or peers.
type Configuration struct {
	Revision uint64
	Groups   []RelayGroup
	Profiles []RelayHomeProfile
	Policies []RelayPairPolicy
}

// Snapshot captures catalog and account data from one database transaction.
type Snapshot struct {
	AccountID     AccountID
	NetworkSerial uint64
	Catalog       RelayCatalog
	Configuration
	Reselections []RelayReselection
}

// Copy returns a detached configuration suitable for mutation previews.
func (c Configuration) Copy() Configuration {
	c.Groups = slices.Clone(c.Groups)
	for i := range c.Groups {
		c.Groups[i] = *c.Groups[i].Copy()
	}
	c.Profiles = slices.Clone(c.Profiles)
	for i := range c.Profiles {
		c.Profiles[i] = *c.Profiles[i].Copy()
	}
	c.Policies = slices.Clone(c.Policies)
	for i := range c.Policies {
		c.Policies[i] = *c.Policies[i].Copy()
	}
	return c
}

// Copy returns a detached snapshot whose nested slices share no mutable state.
func (s Snapshot) Copy() Snapshot {
	s.Configuration = s.Configuration.Copy()
	s.Catalog.Entries = slices.Clone(s.Catalog.Entries)
	for i := range s.Catalog.Entries {
		s.Catalog.Entries[i].Aliases = slices.Clone(s.Catalog.Entries[i].Aliases)
	}
	s.Reselections = slices.Clone(s.Reselections)
	return s
}

// CatalogHead identifies the one authoritative immutable catalog publication.
type CatalogHead struct {
	ID         uint8 `gorm:"primaryKey;autoIncrement:false"`
	Generation uint64
}

// TableName returns the catalog head table name.
func (CatalogHead) TableName() string { return "relay_catalog_heads" }

// CatalogPublication retains an immutable operator-approved catalog generation.
type CatalogPublication struct {
	Generation uint64              `gorm:"primaryKey;autoIncrement:false"`
	Entries    []RelayCatalogEntry `gorm:"serializer:json"`
}

// TableName returns the catalog publication table name.
func (CatalogPublication) TableName() string { return "relay_catalog_publications" }
