import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  canRenderDashboardShell,
  getDashboardLoginDestination,
  getNonEmployeeWorkspaceRedirect,
  getSupportAccountIdentity,
  getSupportAccountLoginDestination,
} from "./identity-boundary.mjs";

const webRoot = new URL("../", import.meta.url);
const read = (path) => readFile(new URL(path, webRoot), "utf8");

test("Support account actions distinguish customers, staff, and unknown identities", () => {
  assert.deepEqual(getSupportAccountIdentity("user"), {
    isCustomer: true,
    isEmployee: false,
    showProfileLinks: true,
    showDashboardLink: false,
    kind: "customer",
  });
  assert.deepEqual(getSupportAccountIdentity("employee"), {
    isCustomer: false,
    isEmployee: true,
    showProfileLinks: false,
    showDashboardLink: true,
    kind: "staff",
  });
  assert.deepEqual(getSupportAccountIdentity(undefined), {
    isCustomer: false,
    isEmployee: false,
    showProfileLinks: false,
    showDashboardLink: false,
    kind: "unknown",
  });
});

test("Support login sends customers to their requested support page and staff to Dashboard", () => {
  assert.equal(getSupportAccountLoginDestination("user", "/support/community/posts/new"), "/support/community/posts/new");
  assert.equal(getSupportAccountLoginDestination("employee", "/support/profile"), "/dashboard");
  assert.equal(getSupportAccountLoginDestination("unrecognized", "/support/profile"), "/support");
});

test("Dashboard password login preserves employee next paths and sends other identities to Support", () => {
  assert.equal(getDashboardLoginDestination("employee", "/dashboard/conversations"), "/dashboard/conversations");
  assert.equal(getDashboardLoginDestination("user", "/dashboard/conversations"), "/support");
  assert.equal(getDashboardLoginDestination(undefined, "/dashboard/conversations"), "/support");
});

test("Dashboard shells render only for employees and send customer or unknown identities to Support", () => {
  assert.equal(canRenderDashboardShell("employee"), true);
  assert.equal(canRenderDashboardShell("user"), false);
  assert.equal(canRenderDashboardShell(undefined), false);
  assert.equal(getNonEmployeeWorkspaceRedirect("employee"), null);
  assert.equal(getNonEmployeeWorkspaceRedirect("user"), "/support");
  assert.equal(getNonEmployeeWorkspaceRedirect(undefined), "/support");
});

test("Support UI and API are wired to the identity boundary", async () => {
  const [supportApi, supportHeader, supportLogin, supportProfile, supportProfileEdit, dashboardLayout, workbenchLayout, loginForm] = await Promise.all([
    read("lib/api/support.ts"),
    read("app/(support)/support/_components/support-header.tsx"),
    read("app/(support)/support/login/_components/login-page.tsx"),
    read("app/(support)/support/profile/_components/profile-page.tsx"),
    read("app/(support)/support/profile/edit/_components/profile-edit-page.tsx"),
    read("app/(dashboard)/dashboard/layout.tsx"),
    read("app/(dashboard)/workbench/layout.tsx"),
    read("components/login-form.tsx"),
  ]);

  assert.match(supportApi, /request<AuthSession>\("\/api\/support\/auth\/login"/);
  assert.match(supportApi, /writeSession\(session\)/);
  assert.doesNotMatch(supportApi, /loginWithPassword/);
  assert.match(supportApi, /\/api\/support\/profile\/update/);
  assert.match(supportApi, /\/api\/support\/profile\/avatar\/upload/);

  assert.match(supportHeader, /if \(!session\) \{[\s\S]*?href="\/support\/login"/);
  assert.match(supportHeader, /getSupportAccountIdentity\(user\.userType\)/);
  assert.match(supportHeader, /identity\.showProfileLinks/);
  assert.match(supportHeader, /identity\.showDashboardLink/);
  assert.match(supportHeader, /supportPublic\.account\.staffIdentity/);
  assert.match(supportLogin, /getSupportAccountLoginDestination\(session\.user\.userType, nextDestination\)/);

  for (const source of [supportProfile, supportProfileEdit]) {
    assert.match(source, /isSupportCustomer\(session\.user\.userType\)/);
    assert.match(source, /router\.replace\("\/support"\)/);
  }
  assert.match(supportProfileEdit, /updateSupportProfile/);
  assert.match(supportProfileEdit, /uploadSupportProfileAvatar/);
  assert.match(dashboardLayout, /canRenderDashboardShell\(session\?\.user\.userType\)/);
  assert.match(dashboardLayout, /getNonEmployeeWorkspaceRedirect\(session\.user\.userType\)/);
  assert.match(workbenchLayout, /canRenderDashboardShell\(session\?\.user\.userType\)/);
  assert.match(workbenchLayout, /getNonEmployeeWorkspaceRedirect\(session\.user\.userType\)/);
  assert.match(loginForm, /getDashboardLoginDestination\(session\.user\.userType, redirectPath\)/);
  assert.match(loginForm, /getDashboardLoginDestination\(authSession\.user\.userType, redirectPath\)/);
});

test("Support account labels exist in both locales", async () => {
  const [zh, en] = await Promise.all([
    read("messages/zh-CN.json").then(JSON.parse),
    read("messages/en-US.json").then(JSON.parse),
  ]);
  for (const key of ["profile", "editProfile", "staffIdentity", "unknownIdentity", "dashboardAction"]) {
    assert.equal(typeof zh.supportPublic.account[key], "string", `zh-CN supportPublic.account.${key}`);
    assert.equal(typeof en.supportPublic.account[key], "string", `en-US supportPublic.account.${key}`);
  }
});
