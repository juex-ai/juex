export type SessionConversationScrollPhase = "hydrate" | "live";

export type SessionConversationScrollOptions = {
  initial: "instant";
  resize: "instant" | "smooth";
};

const SESSION_COMPOSER_MIN_CLEARANCE = 150;
const SESSION_COMPOSER_FADE_HEIGHT = 48;

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
    return SESSION_COMPOSER_MIN_CLEARANCE;
  }
  return Math.max(
    SESSION_COMPOSER_MIN_CLEARANCE,
    Math.ceil(measuredOverlayHeight) + SESSION_COMPOSER_FADE_HEIGHT,
  );
}
