## 🐞 Fixed

- **Skill Marketplace URLs Keep the Forwarded Port** - Marketplace plugin URLs keep a non-default `X-Forwarded-Port`, so a gateway published on a port other than 80 or 443 is no longer rewritten to the scheme default (#7762) (thanks [@hmdsefi](https://github.com/hmdsefi)!)
