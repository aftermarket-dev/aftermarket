# Recorded test results

Written by `go run ./tools/record`. Do not edit by hand.

- Version: 0.1.0
- Recorded: 2026-10-19T15:27:34Z
- Machine: linux/amd64, 2 CPUs, go1.24.7

## Tests

157 of 157 tests passed (race detector on).

| Package | Test | Result |
| --- | --- | --- |
| cmd/aftermarket | TestCheckExample | pass |
| cmd/aftermarket | TestCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestRunNeedsUpstream | pass |
| cmd/aftermarket | TestTestExample | pass |
| cmd/aftermarket | TestTestFailingFixture | pass |
| cmd/aftermarket | TestUsage | pass |
| cmd/aftermarket | TestVersion | pass |
| examples/legacy-customer | TestFixtureFilePasses | pass |
| examples/legacy-customer | TestOldClientNeedsTheRules | pass |
| examples/legacy-customer | TestRemovingEitherRuleBreaksTheClient | pass |
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
| internal/patch | TestFixtureErrors | pass |
| internal/patch | TestFixtureReportsFailures | pass |
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

The legacy-customer example over loopback, 20,000 sequential requests after a 2,000 request warm-up, then 16 clients for 2 seconds.

| Path | p50 | p99 | Requests/s (16 clients) |
| --- | --- | --- | --- |
| direct to upstream | 30.0 µs | 228.8 µs | 36302 |
| proxy, no rule matches | 86.5 µs | 802.8 µs | 9813 |
| proxy, both example rules applied | 134.1 µs | 1413.0 µs | 7863 |

Added by the proxy when no rule matches: 56.5 µs at p50, 574.0 µs at p99.
Added by the proxy with both example rules applied: 104.1 µs at p50, 1184.2 µs at p99.
