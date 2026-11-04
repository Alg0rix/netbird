package admin

import (
	sharedrelay "github.com/netbirdio/netbird/shared/management/relaypolicy"
	"github.com/netbirdio/netbird/shared/management/status"
)

const configurationLimit = 1000

func validateBounds(config sharedrelay.Configuration) error {
	if len(config.Groups) > configurationLimit || len(config.Profiles) > configurationLimit || len(config.Policies) > configurationLimit {
		return oversizedConfiguration()
	}
	for _, group := range config.Groups {
		if len(group.RelayIDs) > configurationLimit {
			return oversizedConfiguration()
		}
	}
	for _, profile := range config.Profiles {
		if err := validateSelectorBounds(profile.Selector); err != nil {
			return err
		}
		if err := validateConstraintBounds(profile.Constraints); err != nil {
			return err
		}
	}
	for _, policy := range config.Policies {
		if err := validateSelectorBounds(policy.Source); err != nil {
			return err
		}
		if err := validateSelectorBounds(policy.Destination); err != nil {
			return err
		}
		if err := validateConstraintBounds(policy.Constraints); err != nil {
			return err
		}
	}
	return nil
}

func validateSelectorBounds(selector sharedrelay.RelaySelector) error {
	if len(selector.PeerIDs) > configurationLimit || len(selector.GroupIDs) > configurationLimit {
		return oversizedConfiguration()
	}
	return nil
}

func validateSelectionBounds(selection sharedrelay.RelaySelection) error {
	if len(selection.RelayIDs) > configurationLimit || len(selection.GroupIDs) > configurationLimit {
		return oversizedConfiguration()
	}
	return nil
}

func validateConstraintBounds(constraints sharedrelay.RelayConstraints) error {
	if len(constraints.Preferences) > 64 {
		return oversizedConfiguration()
	}
	if constraints.Allow != nil {
		if err := validateSelectionBounds(*constraints.Allow); err != nil {
			return err
		}
	}
	if err := validateSelectionBounds(constraints.Exclude); err != nil {
		return err
	}
	for _, tier := range constraints.Preferences {
		if err := validateSelectionBounds(tier); err != nil {
			return err
		}
	}
	return nil
}

func oversizedConfiguration() error {
	return status.Errorf(status.InvalidArgument, "relay configuration exceeds 1000 entries per collection or 64 preference tiers")
}
