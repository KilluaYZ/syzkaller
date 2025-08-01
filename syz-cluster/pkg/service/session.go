// Copyright 2025 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/syzkaller/syz-cluster/pkg/api"
	"github.com/google/syzkaller/syz-cluster/pkg/app"
	"github.com/google/syzkaller/syz-cluster/pkg/blob"
	"github.com/google/syzkaller/syz-cluster/pkg/db"
)

type SessionService struct {
	sessionRepo *db.SessionRepository
	seriesRepo  *db.SeriesRepository
	blobStorage blob.Storage
}

func NewSessionService(env *app.AppEnvironment) *SessionService {
	return &SessionService{
		sessionRepo: db.NewSessionRepository(env.Spanner),
		seriesRepo:  db.NewSeriesRepository(env.Spanner),
		blobStorage: env.BlobStorage,
	}
}

var ErrSessionNotFound = errors.New("session not found")

func (s *SessionService) TriageResult(ctx context.Context, sessionID string, req *api.UploadTriageResultReq) error {
	var triageLogURI string
	if len(req.Log) > 0 {
		var err error
		triageLogURI, err = s.blobStorage.Write(bytes.NewReader(req.Log), "Session", sessionID, "triage_log")
		if err != nil {
			return fmt.Errorf("failed to save the triage log: %w", err)
		}
	}
	err := s.sessionRepo.Update(ctx, sessionID, func(session *db.Session) error {
		session.TriageLogURI = triageLogURI
		if req.SkipReason != "" {
			session.SetSkipReason(req.SkipReason)
		}
		return nil
	})
	if errors.Is(err, db.ErrEntityNotFound) {
		return ErrSessionNotFound
	}
	return err
}

func (s *SessionService) UploadSession(ctx context.Context, req *api.NewSession) (*api.UploadSessionResp, error) {
	series, err := s.seriesRepo.GetByExtID(ctx, req.ExtID)
	if err != nil {
		return nil, err
	} else if series == nil {
		return nil, ErrSeriesNotFound
	}
	session := &db.Session{
		SeriesID:  series.ID,
		Tags:      req.Tags,
		CreatedAt: time.Now(),
	}
	err = s.sessionRepo.Insert(ctx, session)
	if err != nil {
		return nil, err
	}
	return &api.UploadSessionResp{ID: session.ID}, nil
}
