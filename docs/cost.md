# What the build cost

**$0.034 of AWS usage, from 1 to 26 October 2026, before credits.** The account's
credits brought the amount billed to $0.00, so the billed figure says nothing about
what the build used. The usage figure is the one to read.

Pulled from Cost Explorer on 26 October 2026 at 11:29 UTC, and pulled again at
13:36 UTC with the same result, for usage from 2026-10-01 up to but not
including 2026-10-27. Cost Explorer marks the period `Estimated` and lags by up
to a day.

In practice the figure covers usage from the first apply on 23 October through
25 October: the demo page, and the live runs of those days, among them the one
behind `c05-breaker`. It does not include the runs of 26 October, which produced
the three `c09-` bundles. The daily breakdown for 26 October shows no SES usage
yet, only the prorated cost of the stored secret. The figure moves as the month
continues.

| Service | Usage before credits (USD) |
| --- | ---: |
| AWS Secrets Manager (the signing key, and 107 API calls) | 0.031099 |
| Amazon Simple Email Service | 0.002080 |
| Amazon Simple Storage Service (the demo page's bucket) | 0.000515 |
| Amazon API Gateway | 0.000305 |
| Amazon DynamoDB | 0.000143 |
| AWS Lambda | 0.000000 |
| AWS Step Functions | 0.000000 |
| Amazon CloudFront | 0.000000 |
| Amazon CloudWatch | 0.000000 |
| **Reachee total** | **0.034142** |

Lambda, Step Functions, CloudFront and CloudWatch show zero. That is the free
tier, not credits: Cost Explorer reports credits as a separate record type, and
these services show no usage charge before credits either.

## What is left out, and why

The account holds one other project. Cost Explorer reports $1.188 of AWS KMS
usage in the same period. Two customer-managed KMS keys belonging to that
project account for all of it. Reachee creates no KMS key: its signing key is a
Secrets Manager secret, encrypted with the AWS-managed key, whose requests show
no charge. The $1.188 is excluded from the total above. Including it, usage for
the whole account is $1.223, and credits of $1.223 bring it to $0.00.

## Reproduce it

```
aws ce get-cost-and-usage \
  --time-period Start=2026-10-01,End=2026-10-27 \
  --granularity MONTHLY --metrics UnblendedCost \
  --group-by Type=DIMENSION,Key=SERVICE \
  --filter '{"Dimensions":{"Key":"RECORD_TYPE","Values":["Usage"]}}'
```

Without the `RECORD_TYPE` filter, the same command nets credits against usage
and reports every service at or near zero.
