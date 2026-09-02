export type ThreadConversationScrollPhase = "hydrate" | "live";

export type ThreadConversationScrollOptions = {
  initial: "instant";
  resize: "instant" | "smooth";
};

const THREAD_COMPOSER_MIN_CLEARANCE = 150;
const THREAD_COMPOSER_FADE_HEIGHT = 48;

export function threadConversationScrollOptions(
  phase: ThreadConversationScrollPhase = "hydrate",
): ThreadConversationScrollOptions {
  return {
    initial: "instant",
    resize: phase === "hydrate" ? "instant" : "smooth",
  };
}

export function threadComposerClearance(
  measuredOverlayHeight: number,
): number {
  if (!Number.isFinite(measuredOverlayHeight) || measuredOverlayHeight <= 0) {
    return THREAD_COMPOSER_MIN_CLEARANCE;
  }
  return Math.max(
    THREAD_COMPOSER_MIN_CLEARANCE,
    Math.ceil(measuredOverlayHeight) + THREAD_COMPOSER_FADE_HEIGHT,
  );
}
