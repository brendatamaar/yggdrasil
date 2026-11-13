package hosts

import (
	"reflect"
	"strconv"
	"testing"
	"testing/quick"
	"time"
)

func TestResolveInstanceAndTarget(t *testing.T) {
	const authority InstanceID = "git.acme.dev:8443"
	const host InstanceID = "git.acme.dev"
	for mask := 0; mask < 4; mask++ {
		instances := map[InstanceID]Instance{}
		if mask&1 != 0 {
			instances[authority] = Instance{ID: authority}
		}
		if mask&2 != 0 {
			instances[host] = Instance{ID: host}
		}
		want := authority
		if mask == 2 {
			want = host
		}
		if got := ResolveInstance(authority, host, instances); got != want {
			t.Fatalf("mask %d: %s", mask, got)
		}
		for _, branch := range []string{"main", ""} {
			reason := ResolveTarget(RepoTarget{Instance: want, Project: "group/shop", Branch: branch}, instances)
			_, configured := instances[want]
			if (reason == "") != (configured && branch != "") {
				t.Fatalf("mask %d branch %q reason %q", mask, branch, reason)
			}
		}
	}
}

// Exact configured authority is stable under unrelated configuration changes;
// neither resolution helper mutates caller-owned configuration.
func TestPropResolutionPreservesConfiguration(t *testing.T) {
	prop := func(port uint16, branch string) bool {
		authority := InstanceID("git.acme.dev:" + strconv.Itoa(int(port)))
		instances := map[InstanceID]Instance{authority: {ID: authority}, "git.acme.dev": {ID: "git.acme.dev"}}
		before := map[InstanceID]Instance{authority: {ID: authority}, "git.acme.dev": {ID: "git.acme.dev"}}
		got := ResolveInstance(authority, "git.acme.dev", instances)
		reason := ResolveTarget(RepoTarget{Instance: got, Branch: branch}, instances)
		return got == authority && (reason == "") == (branch != "") && reflect.DeepEqual(instances, before)
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPollResultVariants(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	retry := now.Add(time.Minute)
	for _, key := range []Key{RepoKey{Repo: "shop", Branch: "main"}, InboxKey{Instance: "git.acme.dev"}} {
		f := Fetch{Key: key, Attempt: 19}
		payload := Inbox{Items: []InboxItem{{Repo: "group/shop"}}}
		for _, tc := range []struct {
			response PollResponse
			want     Input
		}{
			{PollData{Payload: payload, ETag: "tag"}, Fetched{Key: key, Attempt: 19, Payload: payload, ETag: "tag", Now: now}},
			{PollUnchanged{}, NotModified{Key: key, Attempt: 19, Now: now}},
			{PollUnexpected{}, FetchFailed{Key: key, Attempt: 19, Kind: FailTransient, Now: now}},
		} {
			if got := PollResult(f, tc.response, now); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("%T got %+v want %+v", tc.response, got, tc.want)
			}
		}
		for _, kind := range []FailKind{FailTransient, FailRateLimited, FailUnauthorized, FailNotFound} {
			want := FetchFailed{Key: key, Attempt: 19, Kind: kind, RetryAt: &retry, Now: now}
			if got := PollResult(f, PollFailure{Kind: kind, RetryAt: &retry}, now); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %+v want %+v", got, want)
			}
		}
	}
}

// Every outcome carries the original key, attempt and sampled time. These are
// the fences Step uses to ignore results from obsolete requests (H5).
func TestPropPollResultKeepsRequestIdentity(t *testing.T) {
	prop := func(attempt int, seconds int32, inbox bool) bool {
		var key Key = RepoKey{Repo: "shop", Branch: "feature/cart"}
		if inbox {
			key = InboxKey{Instance: "git.acme.dev"}
		}
		now := time.Unix(int64(seconds), 0)
		f := Fetch{Key: key, Attempt: attempt}
		for _, r := range []PollResponse{PollData{Payload: RepoState{}}, PollUnchanged{}, PollFailure{Kind: FailRateLimited}, PollUnexpected{}} {
			switch v := PollResult(f, r, now).(type) {
			case Fetched:
				if v.Key != key || v.Attempt != attempt || v.Now != now {
					return false
				}
			case NotModified:
				if v.Key != key || v.Attempt != attempt || v.Now != now {
					return false
				}
			case FetchFailed:
				if v.Key != key || v.Attempt != attempt || v.Now != now {
					return false
				}
			case ConfigChanged, TargetsChanged, Tick, RefreshRequested, nil:
				return false
			}
		}
		return true
	}
	if err := quick.Check(prop, nil); err != nil {
		t.Fatal(err)
	}
}
