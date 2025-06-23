import type { NextConfig } from 'next'

const nextConfig: NextConfig = {
  output: 'export',
  trailingSlash: true,
  skipTrailingSlashRedirect: true,
  distDir: 'out',
  basePath: '/ui',
  assetPrefix: '/ui',
  images: {
    unoptimized: true
  }
}

export default nextConfig 