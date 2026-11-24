# Upstream synchronization

## Requirements

### Requirement: Preserve downstream changes

The fork SHALL record the last imported upstream commit.

#### Scenario: Upstream advances

- **WHEN** the upstream branch advances
- **THEN** an update PR includes the upstream delta and its new checkpoint
- **AND** conflicts remain visible for review before integration
