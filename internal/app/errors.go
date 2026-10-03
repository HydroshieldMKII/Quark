package app

import (
	"context"
	"errors"

	"example.org/quark/internal/pdcli"
)

// Action is the next step offered to the user for an error.
type Action int

// Actions.
const (
	ActionNone Action = iota
	ActionRetry
	ActionLogin
	ActionChooseCLI
	ActionRefresh
)

// UserError is what the UI shows for a failure: a title, an explanation,
// an optional technical detail (for an expander) and a suggested action.
type UserError struct {
	Title     string
	Detail    string
	Technical string
	Action    Action
	// Kind is the pdcli kind name, for logs and tests.
	Kind string
	err  error
}

func (e *UserError) Error() string { return e.Title + ": " + e.Detail }
func (e *UserError) Unwrap() error { return e.err }

// errInternal marks a recovered panic or a programming error.
var errInternal = errors.New("internal error")

// Describe turns any error into a UserError. It returns nil for nil and for
// cancellations, which are never shown as errors.
func Describe(err error) *UserError {
	if err == nil || errors.Is(err, context.Canceled) {
		return nil
	}
	var ue *UserError
	if errors.As(err, &ue) {
		return ue
	}
	var pe *pdcli.Error
	if !errors.As(err, &pe) {
		if errors.Is(err, errInternal) {
			return &UserError{Title: T("Something went wrong"), Detail: T("Quark hit an internal error. Details were written to the log."), Technical: err.Error(), Action: ActionRetry, Kind: "internal", err: err}
		}
		return &UserError{Title: T("Something went wrong"), Detail: err.Error(), Action: ActionRetry, Kind: "unknown", err: err}
	}
	u := &UserError{Kind: pe.Kind.String(), Technical: pe.Message, err: err}
	switch pe.Kind {
	case pdcli.KindCancelled:
		return nil
	case pdcli.KindNotLoggedIn:
		u.Title, u.Detail, u.Action = T("You are signed out"), T("Sign in to Proton to continue."), ActionLogin
	case pdcli.KindNetwork:
		u.Title, u.Detail, u.Action = T("Cannot reach Proton"), T("Check your internet connection and try again."), ActionRetry
	case pdcli.KindRateLimited, pdcli.KindServer:
		u.Title, u.Detail, u.Action = T("Proton is busy"), T("Too many requests right now. Try again in a moment."), ActionRetry
	case pdcli.KindTimeout:
		u.Title, u.Detail, u.Action = T("This is taking too long"), T("Proton did not answer in time. Try again."), ActionRetry
	case pdcli.KindNotFound:
		u.Title, u.Detail, u.Action = T("Item not found"), T("It may have been moved, renamed or deleted, possibly on another device."), ActionRefresh
	case pdcli.KindConflict:
		u.Title, u.Detail = T("Name already in use"), T("An item with this name already exists here.")
	case pdcli.KindIntegrity:
		u.Title, u.Detail, u.Action = T("Verification failed"), T("The data could not be verified. Try again."), ActionRetry
	case pdcli.KindDecryption:
		u.Title, u.Detail = T("Cannot decrypt this item"), T("Its keys or signatures could not be verified.")
	case pdcli.KindPartial:
		u.Title, u.Detail, u.Action = T("Some items failed"), T("Not every item could be processed. You can try the failed ones again."), ActionRetry
	case pdcli.KindOutputTooLarge:
		u.Title, u.Detail = T("Folder too large"), T("This folder has too many items for Quark to show.")
	case pdcli.KindBinaryMissing:
		u.Title, u.Detail, u.Action = T("Proton Drive CLI not found"), T("Quark needs the official Proton Drive CLI to work."), ActionChooseCLI
	case pdcli.KindIncompatibleVersion:
		u.Title, u.Detail, u.Action = T("Proton Drive CLI is too old"), pe.Message, ActionChooseCLI
	case pdcli.KindCrash:
		u.Title, u.Detail, u.Action = T("The Proton Drive CLI stopped unexpectedly"), T("Try again. If it keeps happening, check the log."), ActionRetry
		u.Technical = pe.Stderr
	case pdcli.KindBadOutput:
		u.Title, u.Detail, u.Action = T("Unexpected answer from the Proton Drive CLI"), T("Your CLI version may not be supported yet."), ActionRetry
	case pdcli.KindUsage, pdcli.KindRefused:
		u.Title, u.Detail = T("Something went wrong"), T("Quark sent an invalid request. Details were written to the log.")
	default: // KindUnknown
		u.Title, u.Detail, u.Action = T("Something went wrong"), pe.Message, ActionRetry
		if u.Detail == "" {
			u.Detail = T("The Proton Drive CLI reported an error.")
		}
	}
	return u
}
