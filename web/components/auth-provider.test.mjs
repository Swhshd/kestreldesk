// This file has been modified for the KestrelDesk project.

import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const source = await readFile(new URL("./session-provider.tsx", import.meta.url), "utf8");
const authSource = await readFile(new URL("./auth-provider.tsx", import.meta.url), "utf8");

test("refreshSession preserves the stored token when the profile payload omits it", () => {
  assert.match(source, /accessToken:\s*profile\.accessToken\s*\|\|\s*stored\.accessToken/);
  assert.match(source, /expiresAt:\s*profile\.expiresAt\s*\|\|\s*stored\.expiresAt/);
});

test("refreshSession only clears session for explicit auth error codes", () => {
  assert.match(source, /errorCode\s*===\s*3000\s*\|\|\s*errorCode\s*===\s*3002/);
  assert.doesNotMatch(source, /catch\s*\([^)]*\)\s*\{\s*clearSession\(\)/);
});

test("AuthProvider exposes the SessionProvider's stable profile validator", () => {
  assert.match(source, /validateRealtimeProfile:\s*\(\)\s*=>\s*Promise/);
  assert.match(authSource, /validateRealtimeProfile,\s*signOut/);
  assert.match(authSource, /value=\{\{[^}]*validateRealtimeProfile/s);
});
