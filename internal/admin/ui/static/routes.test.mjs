import assert from "node:assert/strict";
import test from "node:test";

import {
  HELP_OVERVIEW_HREF,
  INTELLIGENT_ROUTING_HELP_HREF,
  helpHref,
  parseAdminRoute,
  profileSectionHref,
} from "./routes.js";

test("admin routes distinguish overview setup lists and Profile sections", () => {
  assert.deepEqual(parseAdminRoute("/_admin/"), { page: "overview" });
  assert.deepEqual(parseAdminRoute("/_admin/setup"), { page: "setup" });
  assert.deepEqual(parseAdminRoute("/_admin/profiles"), { page: "profiles" });
	assert.deepEqual(parseAdminRoute("/_admin/stats"), { page: "stats", section: "usage" });
	assert.deepEqual(parseAdminRoute("/_admin/stats/routing"), { page: "stats", section: "routing" });
	assert.deepEqual(parseAdminRoute("/_admin/stats/models"), { page: "stats", section: "models" });
  assert.deepEqual(parseAdminRoute("/_admin/system"), { page: "system" });
  assert.deepEqual(parseAdminRoute(INTELLIGENT_ROUTING_HELP_HREF), {
    page: "help",
    topic: "intelligent-routing",
  });
  assert.deepEqual(parseAdminRoute(HELP_OVERVIEW_HREF), {
    page: "help",
    topic: "overview",
  });
  assert.deepEqual(parseAdminRoute("/_admin/profiles/7/models"), {
    page: "profile",
    profileId: 7,
    section: "models",
  });
});

test("admin routes reject invalid Profile identifiers and unknown sections", () => {
  for (const pathname of [
    "/_admin/profiles/0/models",
    "/_admin/profiles/-1/models",
    "/_admin/profiles/7/unknown",
    "/_admin/profiles/7/models/extra",
    "/_admin/help/unknown",
    "/_admin/help/intelligent-routing/extra",
    "/elsewhere",
  ]) {
    assert.deepEqual(parseAdminRoute(pathname), { page: "not-found" });
  }
});

test("Help links allow only registered secondary topics", () => {
  assert.equal(helpHref("vision"), "/_admin/help/vision");
  assert.equal(helpHref("glossary"), "/_admin/help/glossary");
  assert.throws(() => helpHref("unknown"), /帮助主题/);
});

test("Profile section links encode only safe integer IDs and known sections", () => {
  assert.equal(
    profileSectionHref(7, "routing"),
    "/_admin/profiles/7/routing",
  );
  assert.throws(() => profileSectionHref(0, "routing"), /Profile ID/);
  assert.throws(() => profileSectionHref(7, "unknown"), /Profile 页面/);
});
