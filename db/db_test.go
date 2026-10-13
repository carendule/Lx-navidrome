package db_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/mattn/go-sqlite3"

	"github.com/navidrome/navidrome/db"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/tests"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/pressly/goose/v3"
)

func TestDB(t *testing.T) {
	tests.Init(t, false)
	log.SetLevel(log.LevelFatal)
	RegisterFailHandler(Fail)
	RunSpecs(t, "DB Suite")
}

var _ = DescribeTable("IsBusy",
	func(err error, expected bool) {
		Expect(db.IsBusy(err)).To(Equal(expected))
	},
	Entry("SQLITE_BUSY", sqlite3.Error{Code: sqlite3.ErrBusy}, true),
	Entry("SQLITE_BUSY_SNAPSHOT", sqlite3.Error{Code: sqlite3.ErrBusy, ExtendedCode: sqlite3.ErrBusySnapshot}, true),
	Entry("a wrapped SQLITE_BUSY", fmt.Errorf("persisting: %w", sqlite3.Error{Code: sqlite3.ErrBusy}), true),
	Entry("another SQLite error", sqlite3.Error{Code: sqlite3.ErrConstraint}, false),
	Entry("a non-SQLite error", errors.New("database is locked"), false),
	Entry("nil", nil, false),
)

var _ = Describe("IsSchemaEmpty", func() {
	var database *sql.DB
	var ctx context.Context
	BeforeEach(func() {
		ctx = context.Background()
		path := "file::memory:"
		database, _ = sql.Open(db.Dialect, path)
	})

	It("returns false if the goose metadata table is found", func() {
		_, err := database.Exec("create table goose_db_version (id primary key);")
		Expect(err).ToNot(HaveOccurred())
		Expect(db.IsSchemaEmpty(ctx, database)).To(BeFalse())
	})

	It("returns true if the schema is brand new", func() {
		Expect(db.IsSchemaEmpty(ctx, database)).To(BeTrue())
	})
})

var _ = Describe("out-of-order migrations", func() {
	It("applies missing upstream migrations without resetting permissions", func() {
		ctx := GinkgoT().Context()
		database := newDB(ctx, 0)
		for _, query := range []string{
			`INSERT INTO user (id, user_name, name, email, password, created_at, updated_at,
			 online_search_enabled, online_browser_download_enabled, online_server_download_enabled, online_playlist_sync_enabled)
			 VALUES ('permission-user', 'permission-user', 'Permissions', '', '', datetime('now'), datetime('now'), 0, 1, 0, 1)`,
			`INSERT INTO playlist (id, name, owner_id, visible_to_user_ids, created_at, updated_at)
			 VALUES ('permission-playlist', 'Shared', 'permission-user', '["permission-user"]', datetime('now'), datetime('now'))`,
			`DROP INDEX player_api_key_hash`,
			`ALTER TABLE player DROP COLUMN api_key_hash`,
			`ALTER TABLE library DROP COLUMN pid_album`,
			`ALTER TABLE library DROP COLUMN pid_track`,
			`ALTER TABLE library DROP COLUMN scanned_pid_album`,
			`ALTER TABLE library DROP COLUMN scanned_pid_track`,
			`DELETE FROM goose_db_version WHERE version_id IN (20260922230428, 20260924010054, 20260929221042)`,
		} {
			_, err := database.ExecContext(ctx, query)
			Expect(err).ToNot(HaveOccurred())
		}

		Expect(goose.UpContext(ctx, database, "migrations")).To(MatchError(ContainSubstring("missing migrations")))
		Expect(db.ApplyMigrations(ctx, database)).To(Succeed())
		Expect(db.ApplyMigrations(ctx, database)).To(Succeed())

		var search, browser, server, sync bool
		Expect(database.QueryRowContext(ctx, `SELECT online_search_enabled, online_browser_download_enabled,
		 online_server_download_enabled, online_playlist_sync_enabled FROM user WHERE id = 'permission-user'`).
			Scan(&search, &browser, &server, &sync)).To(Succeed())
		Expect([]bool{search, browser, server, sync}).To(Equal([]bool{false, true, false, true}))
		var visible string
		Expect(database.QueryRowContext(ctx, `SELECT visible_to_user_ids FROM playlist WHERE id = 'permission-playlist'`).Scan(&visible)).To(Succeed())
		Expect(visible).To(Equal(`["permission-user"]`))
		var applied int
		Expect(database.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version
		 WHERE version_id IN (20260922230428, 20260924010054, 20260929221042) AND is_applied = true`).Scan(&applied)).To(Succeed())
		Expect(applied).To(Equal(3))
	})
})
