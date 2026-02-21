package types

type Classification string

const (
	ClassPublic       Classification = "PUBLIC"
	ClassInternal     Classification = "INTERNAL"
	ClassConfidential Classification = "CONFIDENTIAL"
	ClassRestricted   Classification = "RESTRICTED"
)

// ClassificationLevel returns a numeric level for comparison.
// Higher values mean more restricted.
func (c Classification) Level() int {
	switch c {
	case ClassPublic:
		return 0
	case ClassInternal:
		return 1
	case ClassConfidential:
		return 2
	case ClassRestricted:
		return 3
	default:
		return -1
	}
}

// Allows returns true if this classification level permits access to data at the given level.
func (c Classification) Allows(data Classification) bool {
	return c.Level() >= data.Level()
}

func ParseClassification(s string) (Classification, bool) {
	switch Classification(s) {
	case ClassPublic, ClassInternal, ClassConfidential, ClassRestricted:
		return Classification(s), true
	default:
		return "", false
	}
}
