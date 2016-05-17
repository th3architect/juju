// Copyright 2015 Canonical Ltd.
// Licensed under the AGPLv3, see LICENCE file for details.

package persistence

// TODO(ericsnow) Eliminate the mongo-related imports here.

import (
	"github.com/juju/errors"
	"github.com/juju/loggo"
	jujutxn "github.com/juju/txn"
	"gopkg.in/mgo.v2/txn"

	"github.com/juju/juju/payload"
)

var logger = loggo.GetLogger("juju.payload.persistence")

var errNotFound = errors.NewNotFound(nil, "payload")

// TODO(ericsnow) Merge Persistence and EnvPersistence.

// TODO(ericsnow) Store status in the status collection?

// TODO(ericsnow) Move PersistenceBase to the components package?

// PersistenceBase exposes the core persistence functionality needed
// for payloads.
type PersistenceBase interface {
	// All populates docs with the list of the documents corresponding
	// to the provided query.
	All(collName string, query, docs interface{}) error
	// Run runs the transaction generated by the provided factory
	// function. It may be retried several times.
	Run(transactions jujutxn.TransactionSource) error
}

// Persistence exposes the high-level persistence functionality
// related to payloads in Juju.
type Persistence struct {
	db   PersistenceBase
	unit string
}

// NewPersistence builds a new Persistence based on the provided info.
func NewPersistence(db PersistenceBase, unit string) *Persistence {
	return &Persistence{
		db:   db,
		unit: unit,
	}
}

// Track adds records for the payload to persistence. If the payload
// is already there then false gets returned (true if inserted).
// Existing records are not checked for consistency.
func (pp Persistence) Track(id string, pl payload.FullPayloadInfo) error {
	pl.Unit = pp.unit
	logger.Tracef("inserting %q - %#v", id, pl)

	_, err := pp.payloadByStateID(id)
	if err == nil {
		return errors.Annotatef(payload.ErrAlreadyExists, "(ID %q)", id)
	}
	if !errors.IsNotFound(err) {
		return errors.Trace(err)
	}
	// There is a *slight* race here...

	var ops []txn.Op
	// TODO(ericsnow) Add unitPersistence.newEnsureAliveOp(pp.unit)?
	ops = append(ops, pp.newInsertPayloadOps(id, pl)...)
	buildTxn := func(attempt int) ([]txn.Op, error) {
		if attempt > 0 {
			_, err := pp.payloadByName(pl.Name)
			if err == nil {
				return nil, errors.Annotatef(payload.ErrAlreadyExists, "(%s)", pl.FullID())
			}
			if !errors.IsNotFound(err) {
				return nil, errors.Trace(err)
			}
			// Probably a transient error, so try again.
		}
		return ops, nil
	}
	if err := pp.db.Run(buildTxn); err != nil {
		return errors.Trace(err)
	}
	return nil
}

// SetStatus updates the raw status for the identified payload in
// persistence. The return value corresponds to whether or not the
// record was found in persistence. Any other problem results in
// an error. The payload is not checked for inconsistent records.
func (pp Persistence) SetStatus(stID, status string) error {
	logger.Tracef("setting status for %q", stID)

	buildTxn := func(attempt int) ([]txn.Op, error) {
		doc, err := pp.payloadByStateID(stID)
		if errors.IsNotFound(err) {
			return nil, errors.Annotatef(payload.ErrNotFound, "(%s)", stID)
		}
		if err != nil {
			return nil, errors.Trace(err)
		}
		name := doc.Name

		var ops []txn.Op
		// TODO(ericsnow) Add unitPersistence.newEnsureAliveOp(pp.unit)?
		ops = append(ops, pp.newSetRawStatusOps(name, stID, status)...)
		return ops, nil
	}
	if err := pp.db.Run(buildTxn); err != nil {
		return errors.Trace(err)
	}
	return nil
}

// List builds the list of payloads found in persistence which match
// the provided IDs. The lists of IDs with missing records is also
// returned.
func (pp Persistence) List(ids ...string) ([]payload.FullPayloadInfo, []string, error) {
	// TODO(ericsnow) Ensure that the unit is Alive?

	docs, missing, err := pp.payloads(ids)
	if err != nil {
		return nil, nil, errors.Trace(err)
	}

	var results []payload.FullPayloadInfo
	for _, id := range ids {
		doc, ok := docs[id]
		if !ok {
			continue
		}
		p := doc.payload()
		results = append(results, p)
	}
	return results, missing, nil
}

// ListAll builds the list of all payloads found in persistence.
// Inconsistent records result in errors.NotValid.
func (pp Persistence) ListAll() ([]payload.FullPayloadInfo, error) {
	// TODO(ericsnow) Ensure that the unit is Alive?

	docs, err := pp.allPayloads()
	if err != nil {
		return nil, errors.Trace(err)
	}

	var results []payload.FullPayloadInfo
	for _, doc := range docs {
		p := doc.payload()
		results = append(results, p)
	}
	return results, nil
}

// LookUp returns the payload ID for the given name/rawID pair.
func (pp Persistence) LookUp(name, rawID string) (string, error) {
	// TODO(ericsnow) This could be more efficient.

	docs, err := pp.allPayloads()
	if err != nil {
		return "", errors.Trace(err)
	}

	for id, doc := range docs {
		if doc.match(name, rawID) {
			return id, nil
		}
	}

	return "", errors.NotFoundf("payload for %s/%s", name, rawID)
}

// TODO(ericsnow) Add payloads to state/cleanup.go.

// TODO(ericsnow) How to ensure they are completely removed from state
// (when you factor in status stored in a separate collection)?

// Untrack removes all records associated with the identified payload
// from persistence. Also returned is whether or not the payload was
// found. If the records for the payload are not consistent then
// errors.NotValid is returned.
func (pp Persistence) Untrack(stID string) error {
	buildTxn := func(attempt int) ([]txn.Op, error) {
		doc, err := pp.payloadByStateID(stID)
		if errors.IsNotFound(err) {
			// Must have already beeen removed!
			return nil, jujutxn.ErrNoOperations
		}
		if err != nil {
			return nil, errors.Trace(err)
		}
		name := doc.Name

		var ops []txn.Op
		// TODO(ericsnow) Add unitPersistence.newEnsureAliveOp(pp.unit)?
		ops = append(ops, pp.newRemovePayloadOps(name, stID)...)
		return ops, nil
	}
	if err := pp.db.Run(buildTxn); err != nil {
		return errors.Trace(err)
	}
	return nil
}
