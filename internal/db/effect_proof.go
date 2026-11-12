package db

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/brendatamaar/yggdrasil/internal/core/newrepos"
	coreprop "github.com/brendatamaar/yggdrasil/internal/core/proposals"
)

type commentProofJSON struct {
	Kind      string `json:"kind"`
	CommentID string `json:"comment_id"`
	Marker    string `json:"marker"`
	BodyHash  string `json:"body_hash"`
	Head      string `json:"head"`
}

// EncodeCommentReceipt stores comment-specific proof without pretending that a
// posted comment is a new remote repository.
func EncodeCommentReceipt(r coreprop.CommentReceipt) (kind, data string, err error) {
	if err := r.Validate(); err != nil {
		return "", "", err
	}
	b, err := json.Marshal(commentProofJSON{"comment", r.CommentID, r.Marker, r.BodyHash, r.Head})
	return "comment", string(b), err
}

// DecodeCommentReceipt validates the complete, separate comment receipt boundary.
func DecodeCommentReceipt(data string) (coreprop.CommentReceipt, error) {
	var p commentProofJSON
	if err := strictJSON(data, &p); err != nil {
		return coreprop.CommentReceipt{}, err
	}
	if p.Kind != "comment" {
		return coreprop.CommentReceipt{}, errors.New("not a comment receipt")
	}
	r := coreprop.CommentReceipt{CommentID: p.CommentID, Marker: p.Marker, BodyHash: p.BodyHash, Head: p.Head}
	return r, r.Validate()
}

func validateEffectProof(e EffectRecord) error {
	if err := e.State.Validate(); err != nil {
		return err
	}
	if e.Proposal.Validate() != nil || e.Attempt < 1 || e.ID != EffectID(e.Proposal, e.Stage, e.Attempt) {
		return errors.New("effect identity does not match its proposal, stage and attempt")
	}
	var stage newrepos.Stage
	if e.Stage != "" {
		var err error
		stage, err = newrepos.ParseStage(e.Stage)
		if err != nil {
			return err
		}
	}
	if e.State != EffectSucceeded {
		if e.ReceiptKind != "" {
			return errors.New("an unfinished effect cannot carry completed proof")
		}
		var fields map[string]json.RawMessage
		if err := strictJSON(jsonText(e.Receipt, "{}"), &fields); err != nil {
			return err
		}
		if fields == nil || len(fields) != 0 {
			return errors.New("an unfinished effect cannot carry receipt fields")
		}
		return nil
	}
	if e.Stage == "" {
		if e.ReceiptKind != "comment" {
			return errors.New("a comment effect requires a comment receipt")
		}
		proof, err := DecodeCommentReceipt(string(e.Receipt))
		if err != nil {
			return err
		}
		if proof.Marker != e.Marker || proof.CommentID != e.RemoteID {
			return errors.New("comment receipt does not match its effect identity")
		}
		return nil
	}
	proof, err := DecodeReceipt(string(e.Receipt))
	if err != nil {
		return err
	}
	kind, _, err := EncodeReceipt(proof)
	if err != nil {
		return err
	}
	if kind != e.ReceiptKind {
		return errors.New("effect receipt metadata differs from its proof")
	}
	if err := newrepos.ValidateReceiptForStage(stage, proof); err != nil {
		return err
	}
	if remote, ok := proof.(newrepos.RemoteReceipt); ok && remote.RemoteID != e.RemoteID {
		return fmt.Errorf("remote receipt does not match its effect id")
	}
	return nil
}
