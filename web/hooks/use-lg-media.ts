"use client";

import { useSyncExternalStore } from "react";

/** Matches Tailwind's default `lg` breakpoint (1024px). */
const LG_MEDIA_QUERY = "(min-width: 1024px)";

function subscribe(onStoreChange: () => void) {
  const mq = window.matchMedia(LG_MEDIA_QUERY);
  mq.addEventListener("change", onStoreChange);
  return () => mq.removeEventListener("change", onStoreChange);
}

function getSnapshot() {
  return window.matchMedia(LG_MEDIA_QUERY).matches;
}

function getServerSnapshot() {
  return false;
}

/** Client-only true at lg and above. SSR always returns false for mobile-first rendering. */
export function useIsLgUp() {
  return useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
}
