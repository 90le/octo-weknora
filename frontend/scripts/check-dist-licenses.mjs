import { verifyDistributionLicenses } from './distribution-licenses.mjs'

verifyDistributionLicenses(process.argv[2])
console.log('Verified frontend distribution licenses')
