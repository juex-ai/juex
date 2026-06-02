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
