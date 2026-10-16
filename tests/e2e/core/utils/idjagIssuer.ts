import { createSign, generateKeyPairSync, randomUUID, type KeyObject } from 'node:crypto'
import { createServer, type Server } from 'node:http'
import type { AddressInfo } from 'node:net'

/**
 * A throwaway identity provider for enterprise-managed authorization specs. It
 * serves OIDC discovery and a JWKS on 127.0.0.1 and mints ID-JAGs
 * (typ oauth-id-jag+jwt) signed with its own RSA key, standing in for Okta's
 * Cross App Access token exchange.
 */
export class IdJagIssuer {
  private readonly privateKey: KeyObject
  private readonly jwk: Record<string, unknown>
  private server?: Server
  issuer = ''

  constructor() {
    const { privateKey, publicKey } = generateKeyPairSync('rsa', { modulusLength: 2048 })
    this.privateKey = privateKey
    this.jwk = { ...publicKey.export({ format: 'jwk' }), kid: 'e2e-idp', use: 'sig', alg: 'RS256' }
  }

  /** Starts the IdP on an ephemeral loopback port and records its issuer URL. */
  async start(): Promise<string> {
    this.server = createServer((req, res) => {
      res.setHeader('Content-Type', 'application/json')
      if (req.url === '/.well-known/openid-configuration') {
        res.end(JSON.stringify({ issuer: this.issuer, jwks_uri: `${this.issuer}/keys` }))
      } else if (req.url === '/keys') {
        res.end(JSON.stringify({ keys: [this.jwk] }))
      } else {
        res.statusCode = 404
        res.end('{}')
      }
    })
    await new Promise<void>((resolve) => this.server!.listen(0, '127.0.0.1', resolve))
    this.issuer = `http://127.0.0.1:${(this.server!.address() as AddressInfo).port}`
    return this.issuer
  }

  /** Stops the IdP. */
  async stop(): Promise<void> {
    await new Promise<void>((resolve) => (this.server ? this.server.close(() => resolve()) : resolve()))
  }

  /** Mints an ID-JAG for clientID addressed to audience; overrides replace or add claims. */
  mint(audience: string, clientID: string, overrides: Record<string, unknown> = {}): string {
    const now = Math.floor(Date.now() / 1000)
    const header = { alg: 'RS256', typ: 'oauth-id-jag+jwt', kid: 'e2e-idp' }
    const claims = {
      iss: this.issuer,
      sub: '00u-e2e-user',
      aud: audience,
      client_id: clientID,
      jti: randomUUID(),
      iat: now,
      exp: now + 300,
      email: 'e2e-user@example.com',
      ...overrides,
    }
    const encode = (v: unknown) => Buffer.from(JSON.stringify(v)).toString('base64url')
    const signingInput = `${encode(header)}.${encode(claims)}`
    const signature = createSign('RSA-SHA256').update(signingInput).sign(this.privateKey).toString('base64url')
    return `${signingInput}.${signature}`
  }
}
