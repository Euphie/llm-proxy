import assert from "node:assert/strict";
import test from "node:test";

import { parseAdminRoute, profileSectionHref } from "./routes.js";

test("admin routes distinguish overview setup lists and Profile sections", () => {
  assert.deepEqual(parseAdminRoute("/_admin/"), { page: "overview" });
  assert.deepEqual(parseAdminRoute("/_admin/setup"), { page: "setup" });
  assert.deepEqual(parseAdminRoute("/_admin/profiles"), { page: "profiles" });
  assert.deepEqual(parseAdminRoute("/_admin/stats"), { page: "stats" });
  assert.deepEqual(parseAdminRoute("/_admin/system"), { page: "system" });
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
    "/elsewhere",
  ]) {
    assert.deepEqual(parseAdminRoute(pathname), { page: "not-found" });
  }
});

test("Profile section links encode only safe integer IDs and known sections", () => {
  assert.equal(
    profileSectionHref(7, "routing"),
    "/_admin/profiles/7/routing",
  );
  assert.throws(() => profileSectionHref(0, "routing"), /Profile ID/);
  assert.throws(() => profileSectionHref(7, "unknown"), /Profile 页面/);
});
