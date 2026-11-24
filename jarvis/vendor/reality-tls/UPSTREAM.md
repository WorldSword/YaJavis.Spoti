Vendored from https://github.com/cfal/rustls at f1a23c187330386e9e7f14a21dbae368034dc430.

Jarvis changes: connection-scoped ClientHello shaping hook, applied before REALITY authentication and the TLS transcript; retained advertised extension/cipher state follows the shaped hello. Ordinary TLS is unchanged.
