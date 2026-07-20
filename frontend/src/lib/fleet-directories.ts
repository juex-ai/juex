import type {
  DirectoryEntry,
  DirectoryListing,
} from "../types.ts";

export type DirectoryNameValidation = {
  name: string;
  error: string | null;
};

export type WorkspacePathSource = "browser" | "manual";

export function validateNewDirectoryName(
  listing: DirectoryListing,
  value: string,
  showHidden: boolean,
): DirectoryNameValidation {
  const name = value.trim();
  if (!name) {
    return { name, error: "Directory name is required." };
  }
  if (
    name === "." ||
    name === ".." ||
    name.includes("/") ||
    name.includes("\\") ||
    name.includes("\u0000")
  ) {
    return {
      name,
      error: "Directory name must be one path component.",
    };
  }
  if (!showHidden && name.startsWith(".")) {
    return {
      name,
      error: "Turn on Show hidden to create a hidden directory.",
    };
  }
  if (listing.dirs.some((directory) => directory.name === name)) {
    return {
      name,
      error: `A directory named ${name} already exists.`,
    };
  }
  return { name, error: null };
}

export function mergeCreatedDirectory(
  listing: DirectoryListing,
  capturedParent: string,
  created: DirectoryEntry,
): DirectoryListing {
  if (listing.path !== capturedParent) return listing;
  if (listing.dirs.some((directory) => directory.path === created.path)) {
    return listing;
  }
  return {
    ...listing,
    dirs: [...listing.dirs, created].sort((left, right) =>
      left.name.localeCompare(right.name),
    ),
  };
}

export function shouldApplyDirectoryCreateResult({
  requestGeneration,
  currentGeneration,
  capturedParent,
  currentParent,
  dialogOpen,
  draftOpen,
}: {
  requestGeneration: number;
  currentGeneration: number;
  capturedParent: string;
  currentParent: string | undefined;
  dialogOpen: boolean;
  draftOpen: boolean;
}): boolean {
  return (
    requestGeneration === currentGeneration &&
    capturedParent === currentParent &&
    dialogOpen &&
    draftOpen
  );
}

export function directoryCreateKeyAction(
  key: string,
): "create" | "cancel" | null {
  if (key === "Enter") return "create";
  if (key === "Escape") return "cancel";
  return null;
}

export function workspacePathUpdate(
  path: string,
  source: WorkspacePathSource,
): { path: string; revealTail: boolean } {
  return { path, revealTail: source === "browser" };
}

export function revealScrollableTail(
  target: Pick<HTMLElement, "scrollLeft" | "scrollWidth"> | null,
): void {
  if (target) target.scrollLeft = target.scrollWidth;
}
