export function waitForICE(peer: RTCPeerConnection, signal: AbortSignal) {
  if (peer.iceGatheringState === "complete") return Promise.resolve();
  return new Promise<void>((resolve, reject) => {
    const done = () => {
      peer.removeEventListener("icegatheringstatechange", check);
      signal.removeEventListener("abort", abort);
    };
    const abort = () => { done(); reject(signal.reason); };
    const check = () => {
      if (peer.iceGatheringState === "complete") { done(); resolve(); }
    };
    peer.addEventListener("icegatheringstatechange", check);
    signal.addEventListener("abort", abort, { once: true });
    if (signal.aborted) abort();
    else check();
  });
}

export async function gatheredOffer(peer: RTCPeerConnection, signal: AbortSignal) {
  await peer.setLocalDescription(await peer.createOffer());
  await waitForICE(peer, AbortSignal.any([signal, AbortSignal.timeout(8000)]));
  signal.throwIfAborted();
  if (!peer.localDescription) throw new Error("Не удалось сформировать предложение WebRTC");
  return peer.localDescription.toJSON();
}
