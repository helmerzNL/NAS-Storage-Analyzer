// WebAuthn helpers: the server sends options in JSON form (base64url for the
// binary fields); the browser API wants ArrayBuffers and returns them.

export const passkeysSupported = () => typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials;

export function b64urlToBuf(s: string): ArrayBuffer {
  const pad = '='.repeat((4 - (s.length % 4)) % 4);
  const bin = atob((s + pad).replace(/-/g, '+').replace(/_/g, '/'));
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return buf.buffer;
}

export function bufToB64url(b: ArrayBuffer | null | undefined): string | undefined {
  if (!b) return undefined;
  let s = '';
  for (const c of new Uint8Array(b)) s += String.fromCharCode(c);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

export function decodeCreation(options: any): CredentialCreationOptions {
  const pk = options.publicKey;
  pk.challenge = b64urlToBuf(pk.challenge);
  pk.user.id = b64urlToBuf(pk.user.id);
  pk.excludeCredentials = (pk.excludeCredentials ?? []).map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }));
  return { publicKey: pk };
}

export function decodeRequest(options: any): CredentialRequestOptions {
  const pk = options.publicKey;
  pk.challenge = b64urlToBuf(pk.challenge);
  pk.allowCredentials = (pk.allowCredentials ?? []).map((c: any) => ({ ...c, id: b64urlToBuf(c.id) }));
  return { publicKey: pk };
}

export function encodeCreated(c: PublicKeyCredential) {
  const r = c.response as AuthenticatorAttestationResponse;
  return {
    id: c.id, rawId: bufToB64url(c.rawId), type: c.type,
    response: {
      attestationObject: bufToB64url(r.attestationObject), clientDataJSON: bufToB64url(r.clientDataJSON),
      transports: r.getTransports ? r.getTransports() : [],
    },
    clientExtensionResults: c.getClientExtensionResults(),
  };
}

export function encodeAssertion(c: PublicKeyCredential) {
  const r = c.response as AuthenticatorAssertionResponse;
  return {
    id: c.id, rawId: bufToB64url(c.rawId), type: c.type,
    response: {
      authenticatorData: bufToB64url(r.authenticatorData), clientDataJSON: bufToB64url(r.clientDataJSON),
      signature: bufToB64url(r.signature), userHandle: bufToB64url(r.userHandle),
    },
    clientExtensionResults: c.getClientExtensionResults(),
  };
}
