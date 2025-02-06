// Copyright 2024 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package db

import (
	"context"
	"errors"
	"time"

	"cloud.google.com/go/spanner"
	"github.com/google/uuid"
)

type SessionRepository struct {
	client *spanner.Client
	*genericEntityOps[Session, string]
}

func NewSessionRepository(client *spanner.Client) *SessionRepository {
	return &SessionRepository{
		client: client,
		genericEntityOps: &genericEntityOps[Session, string]{
			client:   client,
			keyField: "ID",
			table:    "Sessions",
		},
	}
}

var ErrSessionAlreadyStarted = errors.New("the session already started")

func (repo *SessionRepository) Start(ctx context.Context, sessionID string) error {
	_, err := repo.client.ReadWriteTransaction(ctx,
		func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			iter := txn.Query(ctx, spanner.Statement{
				SQL:    "SELECT * from `Sessions` WHERE `ID`=@id",
				Params: map[string]interface{}{"id": sessionID},
			})
			session, err := readOne[Session](iter)
			iter.Stop()
			if err != nil {
				return err
			}
			if !session.StartedAt.IsNull() {
				return ErrSessionAlreadyStarted
			}
			session.SetStartedAt(time.Now())
			updateSession, err := spanner.UpdateStruct("Sessions", session)
			if err != nil {
				return err
			}
			iter = txn.Query(ctx, spanner.Statement{
				SQL:    "SELECT * from `Series` WHERE `ID`=@id",
				Params: map[string]interface{}{"id": session.SeriesID},
			})
			series, err := readOne[Series](iter)
			iter.Stop()
			if err != nil {
				return err
			}
			series.SetLatestSession(session)
			updateSeries, err := spanner.UpdateStruct("Series", series)
			if err != nil {
				return err
			}
			return txn.BufferWrite([]*spanner.Mutation{updateSeries, updateSession})
		})
	return err
}

func (repo *SessionRepository) Insert(ctx context.Context, session *Session) error {
	if session.ID == "" {
		session.ID = uuid.New().String()
	}
	return repo.genericEntityOps.Insert(ctx, session)
}

func (repo *SessionRepository) ListRunning(ctx context.Context) ([]*Session, error) {
	stmt := spanner.Statement{SQL: "SELECT * FROM `Sessions` WHERE `StartedAt` IS NOT NULL " +
		"AND `FinishedAt` IS NULL"}
	iter := repo.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	return readEntities[Session](iter)
}

type NextSession struct {
	id        string
	createdAt time.Time
}

func (repo *SessionRepository) ListWaiting(ctx context.Context, from *NextSession,
	limit int) ([]*Session, *NextSession, error) {
	// Here we assume that once the session is started, it never appears again.
	stmt := spanner.Statement{
		SQL:    "SELECT * FROM `Sessions` WHERE `StartedAt` IS NULL",
		Params: map[string]interface{}{},
	}
	if from != nil {
		stmt.SQL += " AND ((`CreatedAt` > @from) OR (`CreatedAt` = @from AND `ID` > @id))"
		stmt.Params["from"] = from.createdAt
		stmt.Params["id"] = from.id
	}
	stmt.SQL += " ORDER BY `CreatedAt`, `ID`"
	if limit > 0 {
		stmt.SQL += " LIMIT @limit"
		stmt.Params["limit"] = limit
	}
	iter := repo.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	list, err := readEntities[Session](iter)

	var next *NextSession
	if err == nil && len(list) > 0 {
		last := list[len(list)-1]
		next = &NextSession{
			id:        last.ID,
			createdAt: last.CreatedAt,
		}
	}
	return list, next, err
}

// golint sees too much similarity with SeriesRepository's ListPatches, but in reality there's not.
// nolint:dupl
func (repo *SessionRepository) ListForSeries(ctx context.Context, series *Series) ([]*Session, error) {
	stmt := spanner.Statement{
		SQL:    "SELECT * FROM `Sessions` WHERE `SeriesID` = @series ORDER BY CreatedAt DESC",
		Params: map[string]interface{}{"series": series.ID},
	}
	iter := repo.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	return readEntities[Session](iter)
}
