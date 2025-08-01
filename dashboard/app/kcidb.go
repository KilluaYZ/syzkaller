// Copyright 2020 syzkaller project authors. All rights reserved.
// Use of this source code is governed by Apache 2 LICENSE that can be found in the LICENSE file.

package main

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/google/syzkaller/pkg/kcidb"
	"google.golang.org/appengine/v2"
	db "google.golang.org/appengine/v2/datastore"
	"google.golang.org/appengine/v2/log"
)

func initKcidb() {
	http.HandleFunc("/cron/kcidb_poll", handleKcidbPoll)
}

func handleKcidbPoll(w http.ResponseWriter, r *http.Request) {
	c := appengine.NewContext(r)
	for ns, cfg := range getConfig(c).Namespaces {
		if cfg.Kcidb == nil {
			continue
		}
		if err := handleKcidbNamespce(c, ns, cfg.Kcidb); err != nil {
			log.Errorf(c, "kcidb: %v failed: %v", ns, err)
		}
	}
}

func handleKcidbNamespce(c context.Context, ns string, cfg *KcidbConfig) error {
	client, err := kcidb.NewClient(c, cfg.Origin, cfg.Project, cfg.Topic, cfg.Credentials)
	if err != nil {
		return err
	}
	defer client.Close()

	filter := func(query *db.Query) *db.Query {
		return query.Filter("Namespace=", ns).
			Filter("Status=", BugStatusOpen)
	}
	reported := 0
	return foreachBug(c, filter, func(bug *Bug, bugKey *db.Key) error {
		if reported >= 30 {
			return nil
		}
		ok, err := publishKcidbBug(c, client, bug, bugKey)
		if err != nil {
			return err
		}
		if ok {
			reported++
		}
		return nil
	})
}

func publishKcidbBug(c context.Context, client *kcidb.Client, bug *Bug, bugKey *db.Key) (bool, error) {
	if bug.KcidbStatus != 0 ||
		bug.sanitizeAccess(c, AccessPublic) > AccessPublic ||
		bug.Reporting[len(bug.Reporting)-1].Reported.IsZero() ||
		bug.Status != BugStatusOpen && timeSince(c, bug.LastTime) > 7*24*time.Hour {
		return false, nil
	}
	rep, err := loadBugReport(c, bug)
	if err != nil {
		return false, err
	}
	// publish == false happens only for syzkaller build/test errors.
	// But if this ever happens for a kernel bug, then we also don't want to publish such bugs
	// with missing critical info.
	publish := rep.KernelCommit != "" && len(rep.KernelConfig) != 0

	if publish {
		if err := client.Publish(rep); err != nil {
			return false, err
		}
	}
	tx := func(c context.Context) error {
		bug := new(Bug)
		if err := db.Get(c, bugKey, bug); err != nil {
			return err
		}
		bug.KcidbStatus = 1
		if !publish {
			bug.KcidbStatus = 2
		}
		if _, err := db.Put(c, bugKey, bug); err != nil {
			return fmt.Errorf("failed to put bug: %w", err)
		}
		return nil
	}
	if err := runInTransaction(c, tx, nil); err != nil {
		return false, err
	}
	log.Infof(c, "published bug to kcidb: %v:%v '%v'", bug.Namespace, bugKey.StringID(), bug.displayTitle())
	return publish, nil
}
