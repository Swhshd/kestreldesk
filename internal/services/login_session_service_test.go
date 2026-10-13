package services

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestEmployeeSessionRevokeIsolation(t *testing.T) {
	db, l, user, actor := employeeMutationFixture(t)
	a := employeeMutationLogin(t, db, user.ID, "session-a")
	b := employeeMutationLogin(t, db, user.ID, "session-b")
	a1, a2 := employeeMutationSockets(t, l, user.ID, a.ID, "a")
	b1, b2 := employeeMutationSockets(t, l, user.ID, b.ID, "b")
	calls := 0
	l.onLogin = func(id int64) {
		calls++
		persisted := employeeMutationReadLogin(t, db, id)
		if id != a.ID || persisted.RevokedAt == nil || persisted.UpdateUserID != actor.UserID || persisted.UpdateUserName != actor.Username {
			t.Fatal("exact-session invalidation preceded persistence or used wrong actor")
		}
	}
	if err := LoginSessionService.Revoke(a.ID, actor.UserID, actor.Username); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || !a1.Closed.Load() || !a2.Closed.Load() || b1.Closed.Load() || b2.Closed.Load() {
		t.Fatal("exact-session revoke broke isolation or omitted invalidation")
	}
	if employeeMutationReadLogin(t, db, b.ID).RevokedAt != nil {
		t.Fatal("exact-session revoke persisted sibling revocation")
	}
}

func TestEmployeeSessionRevokeAll(t *testing.T) {
	for _, revoked := range []bool{false, true} {
		t.Run(map[bool]string{false: "active rows", true: "zero updated rows"}[revoked], func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			a := employeeMutationLogin(t, db, user.ID, "session-a")
			b := employeeMutationLogin(t, db, user.ID, "session-b")
			other := employeeMutationLogin(t, db, user.ID+1, "other-employee")
			if revoked {
				for _, id := range []int64{a.ID, b.ID} {
					if err := LoginSessionService.Updates(id, map[string]any{"revoked_at": time.Now()}); err != nil {
						t.Fatal(err)
					}
				}
			}
			beforeA := employeeMutationReadLogin(t, db, a.ID)
			beforeB := employeeMutationReadLogin(t, db, b.ID)
			a1, a2 := employeeMutationSockets(t, l, user.ID, a.ID, "a")
			b1, b2 := employeeMutationSockets(t, l, user.ID, b.ID, "b")
			o1, o2 := employeeMutationSockets(t, l, other.UserID, other.ID, "other")
			calls := 0
			l.onEmployees = func(ids []int64) {
				calls++
				persistedA := employeeMutationReadLogin(t, db, a.ID)
				persistedB := employeeMutationReadLogin(t, db, b.ID)
				if !reflect.DeepEqual(ids, []int64{user.ID}) || persistedA.RevokedAt == nil || persistedB.RevokedAt == nil {
					t.Fatal("revoke-all invalidation preceded persistence or targeted wrong employee")
				}
				if revoked {
					if !reflect.DeepEqual(beforeA, persistedA) || !reflect.DeepEqual(beforeB, persistedB) {
						t.Fatal("revoke-all rewrote already-revoked rows")
					}
				} else if persistedA.UpdateUserID != actor.UserID || persistedA.UpdateUserName != actor.Username || persistedB.UpdateUserID != actor.UserID || persistedB.UpdateUserName != actor.Username {
					t.Fatal("revoke-all lost the existing audit actor")
				}
			}
			if err := LoginSessionService.RevokeByUser(user.ID, actor.UserID, actor.Username); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !a1.Closed.Load() || !a2.Closed.Load() || !b1.Closed.Load() || !b2.Closed.Load() || o1.Closed.Load() || o2.Closed.Load() || employeeMutationReadLogin(t, db, other.ID).RevokedAt != nil {
				t.Fatal("revoke-all omitted employee sockets or affected another employee")
			}
		})
	}
}

func TestEmployeeSessionRevokeWriteFailure(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "all"}[all], func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			row := employeeMutationLogin(t, db, user.ID, "session")
			a, b := employeeMutationSockets(t, l, user.ID, row.ID, "employee")
			failure := errors.New("injected session update failure")
			employeeMutationFailUpdate(t, db, "t_login_session", failure)
			l.onLogin = func(int64) { t.Fatal("failed revoke invalidated") }
			l.onEmployees = func([]int64) { t.Fatal("failed revoke-all invalidated") }
			var err error
			if all {
				err = LoginSessionService.RevokeByUser(user.ID, actor.UserID, actor.Username)
			} else {
				err = LoginSessionService.Revoke(row.ID, actor.UserID, actor.Username)
			}
			if !errors.Is(err, failure) || a.Closed.Load() || b.Closed.Load() || employeeMutationReadLogin(t, db, row.ID).RevokedAt != nil {
				t.Fatal("failed revoke changed persisted/socket state")
			}
		})
	}
}

func TestEmployeePasswordRevokeAll(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(map[bool]string{false: "own", true: "reset"}[reset], func(t *testing.T) {
			db, l, user, actor := employeeMutationFixture(t)
			a := employeeMutationLogin(t, db, user.ID, "a")
			b := employeeMutationLogin(t, db, user.ID, "b")
			a1, a2 := employeeMutationSockets(t, l, user.ID, a.ID, "a")
			b1, b2 := employeeMutationSockets(t, l, user.ID, b.ID, "b")
			calls := 0
			l.onEmployees = func(ids []int64) {
				calls++
				persisted := newUserService(l).Get(user.ID)
				if !reflect.DeepEqual(ids, []int64{user.ID}) || persisted.Password == user.Password || employeeMutationReadLogin(t, db, a.ID).RevokedAt == nil || employeeMutationReadLogin(t, db, b.ID).RevokedAt == nil {
					t.Fatal("password invalidation preceded password/revoke persistence")
				}
			}
			svc := newUserService(l)
			password := "new-password"
			var err error
			if reset {
				password, err = svc.ResetPassword(user.ID, actor)
			} else {
				err = svc.ChangeOwnPassword(password, actor)
			}
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || !a1.Closed.Load() || !a2.Closed.Load() || !b1.Closed.Load() || !b2.Closed.Load() || bcrypt.CompareHashAndPassword([]byte(svc.Get(user.ID).Password), []byte(password)) != nil {
				t.Fatal("password change did not revoke every session and socket")
			}
		})
	}
}
