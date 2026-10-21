# Recorded test results

Written by `go run ./tools/record`. Do not edit by hand.

- Version: 0.5.1
- Recorded: 2026-10-21T13:28:15Z
- Machine: linux/amd64, 2 CPUs, go1.24.7

## Tests

333 of 333 tests passed (race detector on).

| Package | Test | Result |
| --- | --- | --- |
| cmd/aftermarket | TestAdaptCheckAndTest | pass |
| cmd/aftermarket | TestAdaptCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestAdaptRunNeedsUpstream | pass |
| cmd/aftermarket | TestCheckExample | pass |
| cmd/aftermarket | TestCheckReportsErrorsAndOverlaps | pass |
| cmd/aftermarket | TestConfigCommands | pass |
| cmd/aftermarket | TestHookCommands | pass |
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
| examples/legacy-app | TestFixturePasses | pass |
| examples/legacy-app | TestTheExampleEndToEnd | pass |
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
| internal/hook | TestABusyFileDoesNotBlockTheFolder | pass |
| internal/hook | TestAChangeSeenAgainAfterACrashKeepsItsID | pass |
| internal/hook | TestAFileThatBlinksGivesNoEvent | pass |
| internal/hook | TestAQueuedEventWithoutABodyIsRefused | pass |
| internal/hook | TestBaselinesDryRunAndState | pass |
| internal/hook | TestCommandSources | pass |
| internal/hook | TestCommandTimeoutWithAChildHoldingStdout | pass |
| internal/hook | TestDeadLetters | pass |
| internal/hook | TestDeliveryKeepsOrderPerWebhook | pass |
| internal/hook | TestDisplayURLHidesPathsAndQueries | pass |
| internal/hook | TestDryRunPrintsEachChangeOnce | pass |
| internal/hook | TestEveryChangeGivesExactlyOneEvent | pass |
| internal/hook | TestFieldDiffSaysHowManyMore | pass |
| internal/hook | TestFixtureLoading | pass |
| internal/hook | TestFolders | pass |
| internal/hook | TestHTTPSources | pass |
| internal/hook | TestHalfWrittenFilesAreNeverReported | pass |
| internal/hook | TestHelperProcess | pass |
| internal/hook | TestIDsNeverRepeatAcrossBaselines | pass |
| internal/hook | TestLineDiffsKeepSecretsOut | pass |
| internal/hook | TestLoadReportsEveryProblemInLineOrder | pass |
| internal/hook | TestNothingIsLostWhileTheReceiverIsDown | pass |
| internal/hook | TestQueuedEventsKeepTheirBytesAcrossRestarts | pass |
| internal/hook | TestRedactLine | pass |
| internal/hook | TestSecretsStayOut | pass |
| internal/hook | TestSendToNamesEachWebhookOnce | pass |
| internal/hook | TestStoppingMidSendKeepsTheEvent | pass |
| internal/hook | TestTruncatedStructuredFilesWait | pass |
| internal/hook | TestUntilNextWaitsForTheHead | pass |
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
| direct to upstream | 65.7 µs | 311.0 µs | 22003 |
| proxy, no rule matches | 176.3 µs | 1281.6 µs | 5197 |
| proxy, both example rules applied | 242.4 µs | 2254.8 µs | 5066 |
| adapt: direct to v2, one customer | 53.5 µs | 441.4 µs | 21019 |
| adapt: v1 lookup rebuilt from v2 | 200.2 µs | 2062.1 µs | 5941 |
| adapt: direct to v2, list | 65.5 µs | 397.8 µs | 21407 |
| adapt: v1 list rebuilt from v2 | 300.5 µs | 2561.1 µs | 4061 |
| record: v2 lookup saved to a fixture | 244.2 µs | 1916.1 µs | 5942 |

Added by the proxy when no rule matches: 110.6 µs at p50, 970.6 µs at p99.
Added by the proxy with both example rules applied: 176.7 µs at p50, 1943.8 µs at p99.
Added by adapt rebuilding a v1 lookup from v2: 146.7 µs at p50, 1620.7 µs at p99.
Added by adapt rebuilding a v1 list from v2: 235.0 µs at p50, 2163.3 µs at p99.
Added by record saving a v2 lookup to a fixture file: 190.7 µs at p50, 1474.7 µs at p99.
