# Recorded test results

Written by `go run ./tools/record`. Do not edit by hand.

- Version: 0.2.0-dev
- Recorded: 2026-10-20T07:19:14Z
- Machine: linux/amd64, 2 CPUs, go1.24.7

## Tests

217 of 217 tests passed (race detector on).

| Package | Test | Result |
| --- | --- | --- |
| cmd/aftermarket | TestAdaptCheckAndTest | pass |
| cmd/aftermarket | TestAdaptCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestAdaptRunNeedsUpstream | pass |
| cmd/aftermarket | TestCheckExample | pass |
| cmd/aftermarket | TestCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestRunNeedsUpstream | pass |
| cmd/aftermarket | TestTestExample | pass |
| cmd/aftermarket | TestTestFailingFixture | pass |
| cmd/aftermarket | TestUsage | pass |
| cmd/aftermarket | TestVersion | pass |
| examples/customer-api | TestContractIsValidAgainstV1 | pass |
| examples/customer-api | TestEveryEndpointIsNeeded | pass |
| examples/customer-api | TestFixtureFilePasses | pass |
| examples/customer-api | TestOldClientPassesThroughAdapt | pass |
| examples/customer-api | TestV2BreaksTheOldClient | pass |
| examples/legacy-customer | TestFixtureFilePasses | pass |
| examples/legacy-customer | TestOldClientNeedsTheRules | pass |
| examples/legacy-customer | TestRemovingEitherRuleBreaksTheClient | pass |
| internal/adapt | TestAuditNeverContainsValues | pass |
| internal/adapt | TestConcurrencySoak | pass |
| internal/adapt | TestDotSegmentPathsPassThrough | pass |
| internal/adapt | TestDryRunChangesZeroBytes | pass |
| internal/adapt | TestGzipResponseIsRebuiltAndRecompressed | pass |
| internal/adapt | TestHeadersRenameAndCredentialsPassUntouched | pass |
| internal/adapt | TestHealthPath | pass |
| internal/adapt | TestLeftoverQuery | pass |
| internal/adapt | TestLoadErrors | pass |
| internal/adapt | TestLoadGood | pass |
| internal/adapt | TestMatch | pass |
| internal/adapt | TestMethodChangeAndBodyless | pass |
| internal/adapt | TestMissingFieldsAreLeftOutAndLogged | pass |
| internal/adapt | TestMissingListIsDeduplicated | pass |
| internal/adapt | TestOverlaps | pass |
| internal/adapt | TestQueryStringsKeepTheirBytes | pass |
| internal/adapt | TestRequestThatCannotBeTranslatedPassesUnchanged | pass |
| internal/adapt | TestResponseIsAllOrNothing | pass |
| internal/adapt | TestSlowUpstreamAndStalledBodyGive504 | pass |
| internal/adapt | TestTemplateCannotBeBuilt | pass |
| internal/adapt | TestTemplateEach | pass |
| internal/adapt | TestTemplateMissingFieldsAreLeftOut | pass |
| internal/adapt | TestTemplateObject | pass |
| internal/adapt | TestTemplateWithoutBody | pass |
| internal/adapt | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/adapt | TestUnsafeValuesNeverReachThePath | pass |
| internal/adapt | TestUpstreamDownGives502 | pass |
| internal/fixture | TestFixtureErrors | pass |
| internal/fixture | TestFixtureReportsFailures | pass |
| internal/ojson | TestCloneIsDeep | pass |
| internal/ojson | TestDecodeKeepsRealReplacementCharacters | pass |
| internal/ojson | TestDecodeRejects | pass |
| internal/ojson | TestParsePath | pass |
| internal/ojson | TestPathOps | pass |
| internal/ojson | TestRename | pass |
| internal/ojson | TestRoundTripKeepsOrderAndNumbers | pass |
| internal/patch | Test204AuditSaysNoBody | pass |
| internal/patch | TestAuditNeverContainsValues | pass |
| internal/patch | TestBodiesThatWouldNotRoundTripAreSkipped | pass |
| internal/patch | TestBrokenGzipPassesUnchanged | pass |
| internal/patch | TestChangedBodyDropsTrailersAndDigests | pass |
| internal/patch | TestChunkedResponseIsPatched | pass |
| internal/patch | TestConcurrencySoak | pass |
| internal/patch | TestConnectionListedHeadersAreDroppedWhenARuleMatches | pass |
| internal/patch | TestDryRunChangesZeroBytes | pass |
| internal/patch | TestForwardingHeadersCanBeChanged | pass |
| internal/patch | TestGzipContentLengthIsCorrected | pass |
| internal/patch | TestGzipResponseIsPatchedAndRecompressed | pass |
| internal/patch | TestHealthPath | pass |
| internal/patch | TestMalformedJSONPassesUnchanged | pass |
| internal/patch | TestMalformedRequestBodyPassesUnchanged | pass |
| internal/patch | TestMatchedResponseIsPatched | pass |
| internal/patch | TestNewValidatesUpstream | pass |
| internal/patch | TestNoBodyResponsesAreSkipped | pass |
| internal/patch | TestNullBodyIsUnchangedNotSkipped | pass |
| internal/patch | TestOddQueryStringsPassThroughExactly | pass |
| internal/patch | TestOversizeBodyPassesUnchanged | pass |
| internal/patch | TestPartlyAppliedRuleIsLoggedAsChanged | pass |
| internal/patch | TestPathRulesIgnoreDotSegments | pass |
| internal/patch | TestRequestIsPatched | pass |
| internal/patch | TestRewriteCannotEscapeItsPrefix | pass |
| internal/patch | TestRewriteNeedsItsQueryParameter | pass |
| internal/patch | TestRuleOrderIsDeterministic | pass |
| internal/patch | TestSlowUpstreamGives504 | pass |
| internal/patch | TestStalledBodyGives504 | pass |
| internal/patch | TestUnmatchedResponseStreams | pass |
| internal/patch | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/patch | TestUnsupportedEncodingPassesUnchanged | pass |
| internal/patch | TestUpgradePassesThrough | pass |
| internal/patch | TestUpstreamBasePath | pass |
| internal/patch | TestUpstreamDownGives502 | pass |
| internal/patch | TestUpstreamErrorReasonHasNoValues | pass |
| internal/rules | TestGlobMatch | pass |
| internal/rules | TestLoadErrors | pass |
| internal/rules | TestLoadGood | pass |
| internal/rules | TestMatching | pass |
| internal/rules | TestOverlaps | pass |
| internal/rules | TestParseSize | pass |
| internal/rules | TestPathGlob | pass |
| internal/rules | TestReportsAllErrors | pass |
| internal/rules | TestRewriteGroupReferences | pass |
| internal/yamlite | TestLineNumbers | pass |
| internal/yamlite | TestParseErrors | pass |
| internal/yamlite | TestParseValid | pass |

## Latency

Over loopback, 20,000 sequential requests per row after a 2,000 request warm-up, then 16 clients for 2 seconds. The first three rows are patch with the legacy-customer example; the adapt rows are the customer-api example, where the list has three customers.

| Path | p50 | p99 | Requests/s (16 clients) |
| --- | --- | --- | --- |
| direct to upstream | 26.1 µs | 147.8 µs | 44274 |
| proxy, no rule matches | 85.8 µs | 713.6 µs | 11344 |
| proxy, both example rules applied | 110.2 µs | 1273.0 µs | 9238 |
| adapt: direct to v2, one customer | 27.5 µs | 215.3 µs | 40233 |
| adapt: v1 lookup rebuilt from v2 | 104.9 µs | 1436.3 µs | 9868 |
| adapt: direct to v2, list | 28.7 µs | 206.5 µs | 39508 |
| adapt: v1 list rebuilt from v2 | 131.1 µs | 1596.7 µs | 8087 |

Added by the proxy when no rule matches: 59.7 µs at p50, 565.8 µs at p99.
Added by the proxy with both example rules applied: 84.1 µs at p50, 1125.2 µs at p99.
Added by adapt rebuilding a v1 lookup from v2: 77.4 µs at p50, 1221.0 µs at p99.
Added by adapt rebuilding a v1 list from v2: 102.4 µs at p50, 1390.2 µs at p99.
