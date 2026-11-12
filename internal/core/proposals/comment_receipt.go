package proposals

import "strings"

// CommentReceipt proves one review comment was posted for its immutable marker,
// body and reviewed head. It is distinct from new-repo stage receipts.
type CommentReceipt struct {
	CommentID string
	Marker    string
	BodyHash  string
	Head      string
}

// Validate rejects incomplete or ambiguous comment recovery evidence.
func (r CommentReceipt) Validate() error {
	if r.CommentID == "" || len(r.CommentID) > 200 || hasControl(r.CommentID) || strings.ContainsAny(r.CommentID, " /\\?:@#") {
		return invalid("the comment receipt needs a plain host comment id")
	}
	if err := checkMarker(r.Marker); err != nil {
		return err
	}
	if err := checkHexField("comment body hash", r.BodyHash, false); err != nil {
		return err
	}
	if len(r.BodyHash) != 64 {
		return invalid("the comment receipt needs a full body checksum")
	}
	if err := checkHexField("comment head", r.Head, false); err != nil {
		return err
	}
	return nil
}
