import { beforeEach, describe, expect, it } from "vitest";
import {
  clearWorkspaceLayoutForUser,
  loadWorkspaceLayoutForUser,
  saveWorkspaceLayoutForUser,
} from "./workspaceLayoutPersistence";

describe("workspace layout persistence (localStorage-backed)", () => {
  beforeEach(() => {
    window.localStorage.clear();
  });

  it("returns null for a user with no saved layout yet", () => {
    expect(loadWorkspaceLayoutForUser("acct-999")).toBeNull();
  });

  it("round-trips a saved layout for a given user", () => {
    const fakeLayoutJson = JSON.stringify({ root: { type: "row", content: [] } });
    const wasSaved = saveWorkspaceLayoutForUser("acct-001", fakeLayoutJson);
    expect(wasSaved).toBe(true);
    expect(loadWorkspaceLayoutForUser("acct-001")).toBe(fakeLayoutJson);
  });

  it("keeps different users' saved layouts independent", () => {
    saveWorkspaceLayoutForUser("acct-001", JSON.stringify({ owner: "acct-001" }));
    saveWorkspaceLayoutForUser("acct-002", JSON.stringify({ owner: "acct-002" }));

    expect(JSON.parse(loadWorkspaceLayoutForUser("acct-001")!)).toEqual({ owner: "acct-001" });
    expect(JSON.parse(loadWorkspaceLayoutForUser("acct-002")!)).toEqual({ owner: "acct-002" });
  });

  it("clears a saved layout for one user without touching another's", () => {
    saveWorkspaceLayoutForUser("acct-001", JSON.stringify({ owner: "acct-001" }));
    saveWorkspaceLayoutForUser("acct-002", JSON.stringify({ owner: "acct-002" }));

    clearWorkspaceLayoutForUser("acct-001");

    expect(loadWorkspaceLayoutForUser("acct-001")).toBeNull();
    expect(loadWorkspaceLayoutForUser("acct-002")).not.toBeNull();
  });

  it("overwrites a previously saved layout for the same user", () => {
    saveWorkspaceLayoutForUser("acct-001", JSON.stringify({ version: 1 }));
    saveWorkspaceLayoutForUser("acct-001", JSON.stringify({ version: 2 }));
    expect(JSON.parse(loadWorkspaceLayoutForUser("acct-001")!)).toEqual({ version: 2 });
  });
});
