# Recorded test results

Written by `go run ./tools/record`. Do not edit by hand.

- Version: 0.4.0
- Recorded: 2026-10-21T11:34:17Z
- Machine: linux/amd64, 2 CPUs, go1.24.7

## Tests

296 of 296 tests passed (race detector on).

| Package | Test | Result |
| --- | --- | --- |
| cmd/aftermarket | TestAdaptCheckAndTest | pass |
| cmd/aftermarket | TestAdaptCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestAdaptRunNeedsUpstream | pass |
| cmd/aftermarket | TestCheckExample | pass |
| cmd/aftermarket | TestCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestConfigCommands | pass |
| cmd/aftermarket | TestRecordCheck | pass |
| cmd/aftermarket | TestRecordNeverOverwrites | pass |
| cmd/aftermarket | TestRecordOnBusyPortLeavesNoFile | pass |
| cmd/aftermarket | TestReplayCLI | pass |
| cmd/aftermarket | TestRunNeedsUpstream | pass |
| cmd/aftermarket | TestTestExample | pass |
| cmd/aftermarket | TestTestFailingFixture | pass |
| cmd/aftermarket | TestUsage | pass |
| cmd/aftermarket | TestVersion | pass |
| examples/customer-api | TestContractIsValidAgainstV1 | pass |
| examples/customer-api | TestEveryEndpointIsNeeded | pass |
| examples/customer-api | TestFixtureFilePasses | pass |
| examples/customer-api | TestOldClientPassesThroughAdapt | pass |
| examples/customer-api | TestRecordedSuiteReplaysTheSameThroughAdapt | pass |
| examples/customer-api | TestV2BreaksTheOldClient | pass |
| examples/legacy-customer | TestFixtureFilePasses | pass |
| examples/legacy-customer | TestOldClientNeedsTheRules | pass |
| examples/legacy-customer | TestRemovingEitherRuleBreaksTheClient | pass |
| examples/vendor-app | TestFixturePasses | pass |
| examples/vendor-app | TestUpgradeStopsWhereTheVendorChangedSomething | pass |
| internal/adapt | TestAuditNeverContainsValues | pass |
| internal/adapt | TestCheckReportsEveryProblemInLineOrder | pass |
| internal/adapt | TestChunkedRequestBodyIsDroppedForGet | pass |
| internal/adapt | TestConcurrencySoak | pass |
| internal/adapt | TestDotSegmentPathsPassThrough | pass |
| internal/adapt | TestDryRunChangesZeroBytes | pass |
| internal/adapt | TestDryRunCoversEveryKindOfChange | pass |
| internal/adapt | TestDryRunNeverTurnsSlowAnswersInto504 | pass |
| internal/adapt | TestExplicitHeadersWinOverRebuildDefaults | pass |
| internal/adapt | TestGzipResponseIsRebuiltAndRecompressed | pass |
| internal/adapt | TestHeaderMapOverlapsAreRejected | pass |
| internal/adapt | TestHeadersRenameAndCredentialsPassUntouched | pass |
| internal/adapt | TestHealthPath | pass |
| internal/adapt | TestHugeNumbersAreRejected | pass |
| internal/adapt | TestInvalidUTF8ParameterIsSkipped | pass |
| internal/adapt | TestLeftoverQuery | pass |
| internal/adapt | TestLeftoverQueryNeverOverridesTranslatedKeys | pass |
| internal/adapt | TestLoadErrors | pass |
| internal/adapt | TestLoadGood | pass |
| internal/adapt | TestMatch | pass |
| internal/adapt | TestMethodChangeAndBodyless | pass |
| internal/adapt | TestMissingFieldsAreLeftOutAndLogged | pass |
| internal/adapt | TestMissingListIsDeduplicated | pass |
| internal/adapt | TestOverlaps | pass |
| internal/adapt | TestQueryStringsKeepTheirBytes | pass |
| internal/adapt | TestRenameNeverOverwritesExistingValues | pass |
| internal/adapt | TestRepeatedParameterChecksHeadersFirst | pass |
| internal/adapt | TestRequestThatCannotBeTranslatedPassesUnchanged | pass |
| internal/adapt | TestResponseIsAllOrNothing | pass |
| internal/adapt | TestResponseSkipLeavesHeadersAlone | pass |
| internal/adapt | TestSkippedMatchThenUpstreamErrorLogsPatternAndSameID | pass |
| internal/adapt | TestSlowUpstreamAndStalledBodyGive504 | pass |
| internal/adapt | TestStalledRequestBodyGets408 | pass |
| internal/adapt | TestTemplateCannotBeBuilt | pass |
| internal/adapt | TestTemplateEach | pass |
| internal/adapt | TestTemplateMissingFieldsAreLeftOut | pass |
| internal/adapt | TestTemplateObject | pass |
| internal/adapt | TestTemplateWithoutBody | pass |
| internal/adapt | TestUnmatchedResponseStreams | pass |
| internal/adapt | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/adapt | TestUnsafeValuesNeverReachThePath | pass |
| internal/adapt | TestUpstreamDownGives502 | pass |
| internal/config | TestApplyTwiceGivesTheSameBytes | pass |
| internal/config | TestDeepNestingIsRefused | pass |
| internal/config | TestEnvFilesKeepEveryLineTheOverlayDoesntChange | pass |
| internal/config | TestEnvRewritesKeepMeaning | pass |
| internal/config | TestFailedWritesLeaveTheOldOutput | pass |
| internal/config | TestFieldsTheOverlayDoesntNameKeepOrderAndValue | pass |
| internal/config | TestFixtureFailuresHideSecrets | pass |
| internal/config | TestFixtures | pass |
| internal/config | TestFormatOf | pass |
| internal/config | TestGuardsStopApply | pass |
| internal/config | TestLoadReportsEveryProblemInLineOrder | pass |
| internal/config | TestRandomDocumentsKeepEverythingElse | pass |
| internal/config | TestRenameIntoItselfIsRefused | pass |
| internal/config | TestSecretsAreNeverPrinted | pass |
| internal/config | TestSecretsStayHiddenWhenMovedOrNamedDifferently | pass |
| internal/config | TestTheBaseIsNeverWritten | pass |
| internal/config | TestTopLevelListDelete | pass |
| internal/config | TestUnchangedFilesComeBackExactly | pass |
| internal/config | TestYAMLColonsAndKeysReadBack | pass |
| internal/config | TestYAMLVendorFile | pass |
| internal/config | TestYAMLWriterReadsBack | pass |
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
| internal/patch | TestDryRunNeverTurnsSlowAnswersInto504 | pass |
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
| internal/patch | TestStalledRequestBodyGets408 | pass |
| internal/patch | TestUnmatchedResponseStreams | pass |
| internal/patch | TestUnmatchedTrafficIsByteIdentical | pass |
| internal/patch | TestUnsupportedEncodingPassesUnchanged | pass |
| internal/patch | TestUpgradePassesThrough | pass |
| internal/patch | TestUpstreamBasePath | pass |
| internal/patch | TestUpstreamDownGives502 | pass |
| internal/patch | TestUpstreamErrorReasonHasNoValues | pass |
| internal/record | TestAuditIDsAndMaxCases | pass |
| internal/record | TestConcurrentRecording | pass |
| internal/record | TestConfigErrorsAreAllReportedInLineOrder | pass |
| internal/record | TestGzipBodiesAreStoredDecoded | pass |
| internal/record | TestHeadersThatArentTextAreSkipped | pass |
| internal/record | TestHealthPath | pass |
| internal/record | TestJSONIsRedactedWhateverTheContentType | pass |
| internal/record | TestQueryRedactionCoversEverySeparator | pass |
| internal/record | TestQuoteRoundTrips | pass |
| internal/record | TestRecordingChangesZeroBytes | pass |
| internal/record | TestRecordingsRunWithPatchAndAdaptTest | pass |
| internal/record | TestRedaction | pass |
| internal/record | TestRequestWithoutPathIsStoredAsSlash | pass |
| internal/record | TestResponsesStream | pass |
| internal/record | TestUnstorableExchangesPassAndAreSkipped | pass |
| internal/record | TestUpgradedConnectionsPassThrough | pass |
| internal/record | TestUpstreamDownAndSlow | pass |
| internal/record | TestUpstreamURLSecretsStayOut | pass |
| internal/record | TestWhenPicksTrafficAndMaxCasesStops | pass |
| internal/replay | TestBadOptions | pass |
| internal/replay | TestDifferencesAreListedByPath | pass |
| internal/replay | TestDiffsAreCapped | pass |
| internal/replay | TestGzipAnswersAreComparedDecoded | pass |
| internal/replay | TestJSONIsComparedByMeaning | pass |
| internal/replay | TestNothingComparedIsNotOK | pass |
| internal/replay | TestNumbersAreComparedExactly | pass |
| internal/replay | TestOneServiceIsComparedWithTheRecording | pass |
| internal/replay | TestOtherBodiesAreComparedByteForByte | pass |
| internal/replay | TestReportHidesURLSecrets | pass |
| internal/replay | TestReports | pass |
| internal/replay | TestRequestsAreSentAsRecorded | pass |
| internal/replay | TestUnreachableServiceIsAnError | pass |
| internal/replay | TestWritesAreSentOnlyWhenAsked | pass |
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

Over loopback, 20,000 sequential requests per row after a 2,000 request warm-up, then 16 clients for 2 seconds. The first three rows are patch with the legacy-customer example; the adapt rows are the customer-api example, where the list has three customers. The record row saves every exchange to a fixture file on disk.

| Path | p50 | p99 | Requests/s (16 clients) |
| --- | --- | --- | --- |
| direct to upstream | 56.8 µs | 303.1 µs | 26028 |
| proxy, no rule matches | 165.4 µs | 1124.5 µs | 6693 |
| proxy, both example rules applied | 209.6 µs | 1976.5 µs | 5972 |
| adapt: direct to v2, one customer | 56.1 µs | 378.0 µs | 22634 |
| adapt: v1 lookup rebuilt from v2 | 224.4 µs | 2395.2 µs | 5761 |
| adapt: direct to v2, list | 49.1 µs | 365.9 µs | 22594 |
| adapt: v1 list rebuilt from v2 | 259.8 µs | 2600.9 µs | 4928 |
| record: v2 lookup saved to a fixture | 235.8 µs | 1751.0 µs | 4747 |

Added by the proxy when no rule matches: 108.6 µs at p50, 821.4 µs at p99.
Added by the proxy with both example rules applied: 152.8 µs at p50, 1673.4 µs at p99.
Added by adapt rebuilding a v1 lookup from v2: 168.3 µs at p50, 2017.2 µs at p99.
Added by adapt rebuilding a v1 list from v2: 210.7 µs at p50, 2235.0 µs at p99.
Added by record saving a v2 lookup to a fixture file: 179.7 µs at p50, 1373.0 µs at p99.
