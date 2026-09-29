package githubclient

// Review is where a PR stands with its reviewers, reduced to what the
// dashboard shows. It is separate from the Stoplight, which is CI only.
type Review int

const (
	ReviewNone             Review = iota // no review signal at all
	ReviewWaiting                        // a review is required or requested, none given yet
	ReviewChangesRequested               // a reviewer asked for changes
	ReviewApproved                       // approved
)

func (r Review) String() string {
	switch r {
	case ReviewWaiting:
		return "waiting"
	case ReviewChangesRequested:
		return "changes requested"
	case ReviewApproved:
		return "approved"
	default:
		return "none"
	}
}

// deriveReview reduces a PR's review fields to a Review. GitHub only sets
// reviewDecision when branch protection requires reviews, so on unprotected
// branches it falls back to each reviewer's latest review and any pending
// review requests. As on GitHub, a request for changes outranks an approval.
func deriveReview(decision string, latest []string, pendingRequests int) Review {
	switch decision {
	case "APPROVED":
		return ReviewApproved
	case "CHANGES_REQUESTED":
		return ReviewChangesRequested
	case "REVIEW_REQUIRED":
		return ReviewWaiting
	}

	approved := false
	for _, state := range latest {
		switch state {
		case "CHANGES_REQUESTED":
			return ReviewChangesRequested
		case "APPROVED":
			approved = true
		}
	}
	if approved {
		return ReviewApproved
	}
	if pendingRequests > 0 {
		return ReviewWaiting
	}
	return ReviewNone
}
