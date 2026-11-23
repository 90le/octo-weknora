import type { Plugin } from 'vite'

export const npmNoticePath: string
export function projectLicenseBanner(): string
export function distributionLicenses(): Plugin
export function verifyDistributionLicenses(directory?: string): void
