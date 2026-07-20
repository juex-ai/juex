export type SessionConversationScrollPhase = "hydrate" | "live";

export type SessionConversationScrollOptions = {
  initial: "instant";
  resize: "instant" | "smooth";
};

export function sessionConversationScrollOptions(
  phase: SessionConversationScrollPhase = "hydrate",
): SessionConversationScrollOptions {
  return {
    initial: "instant",
    resize: phase === "hydrate" ? "instant" : "smooth",
  };
}

export function sessionComposerClearance(
  measuredOverlayHeight: number,
): number {
  if (!Number.isFinite(measuredOverlayHeight) || measuredOverlayHeight <= 0) {
    return 150;
  }
  return Math.max(150, Math.ceil(measuredOverlayHeight) + 12);
}
