package com.remohop.app

import com.remohop.app.PageRecovery.AfterFailure
import com.remohop.app.PageRecovery.Foreground
import com.remohop.app.PageRecovery.Phase
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PageRecoveryTest {
    private val o = "http://127.0.0.1:41234"

    @Test fun coveredUntilTheFirstLoadFinishes() {
        val r = PageRecovery()
        assertTrue(r.covered)
        assertEquals(Foreground.NOTHING, r.foregroundAction())
        r.loadStarted()
        assertTrue(r.covered)
        assertEquals(Foreground.NOTHING, r.foregroundAction())
        r.loadFinished()
        assertFalse(r.covered)
        assertEquals(Foreground.PROBE, r.foregroundAction())
    }

    @Test fun goneInTheForegroundReloadsAtOnce() {
        val r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        assertTrue(r.processTerminated(inBackground = false))
        assertTrue(r.covered)
    }

    @Test fun goneInTheBackgroundReloadsOnReturn() {
        val r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        assertFalse(r.processTerminated(inBackground = true))
        assertEquals(Phase.DEAD, r.phase)
        assertTrue(r.covered)
        assertEquals(Foreground.RELOAD, r.foregroundAction())
    }

    @Test fun aLoadCutOffByTheBackgroundIsDoneAgain() {
        val r = PageRecovery()
        r.loadStarted()
        r.enteredBackground()
        assertEquals(Foreground.RELOAD, r.foregroundAction())
        // A shown page stays shown.
        val s = PageRecovery()
        s.loadStarted()
        s.loadFinished()
        s.enteredBackground()
        assertEquals(Foreground.PROBE, s.foregroundAction())
    }

    @Test fun failuresRetryWithBackoffThenGiveUp() {
        val r = PageRecovery()
        for ((i, wait) in listOf(1_000L, 2_000L, 4_000L).withIndex()) {
            r.loadStarted()
            assertEquals("failure ${i + 1}", AfterFailure.Retry(wait), r.loadFailed(inBackground = false))
            assertTrue(r.retryDue(r.generation))
        }
        r.loadStarted()
        assertEquals(AfterFailure.GiveUp, r.loadFailed(inBackground = false))
        assertEquals(Phase.FAILED, r.phase)
        assertTrue(r.covered)
        // Back in the foreground (or Try again): a fresh round.
        assertEquals(Foreground.RELOAD, r.foregroundAction())
        r.retryNow()
        r.loadStarted()
        assertEquals(AfterFailure.Retry(1_000L), r.loadFailed(inBackground = false))
        r.loadStarted()
        r.loadFinished()
        assertEquals(0, r.failures)
    }

    @Test fun aFailureInTheBackgroundWaitsForTheForeground() {
        val r = PageRecovery()
        r.loadStarted()
        assertEquals(AfterFailure.WaitForForeground, r.loadFailed(inBackground = true))
        assertEquals(0, r.failures)
        assertEquals(Foreground.RELOAD, r.foregroundAction())
    }

    @Test fun lateEventsOfAnEarlierLoadAreIgnored() {
        val r = PageRecovery()
        r.loadStarted()
        val first = r.generation
        r.loadStarted()
        assertEquals(AfterFailure.Ignored, r.loadTimedOut(first, inBackground = false))
        assertEquals(Phase.LOADING, r.phase)
        assertEquals(AfterFailure.Retry(1_000L), r.loadTimedOut(r.generation, inBackground = false))
        // A retry timer is stale once another load started.
        val failed = r.generation
        r.loadStarted()
        assertFalse(r.retryDue(failed))
        // A failure of a page that is shown (a navigation the page started) leaves it.
        r.loadFinished()
        assertEquals(AfterFailure.Ignored, r.loadFailed(inBackground = false))
        assertEquals(Phase.READY, r.phase)
    }

    @Test fun probeFailureOnlyCountsForTheShownPage() {
        val r = PageRecovery()
        r.loadStarted()
        r.loadFinished()
        val gen = r.generation
        assertTrue(r.probeFailed(gen))
        assertEquals(Phase.DEAD, r.phase)
        assertFalse(r.probeFailed(gen))
        r.loadStarted()
        r.loadFinished()
        assertFalse(r.probeFailed(gen))
    }

    @Test fun probeResult() {
        assertTrue(PageRecovery.probeOk("$o|complete|3", o))
        assertTrue(PageRecovery.probeOk("$o|interactive|1", o))
        assertFalse(PageRecovery.probeOk("$o|loading|3", o))
        assertFalse(PageRecovery.probeOk("$o|complete|0", o))
        assertFalse(PageRecovery.probeOk("null|complete|3", o))
        assertFalse(PageRecovery.probeOk("http://127.0.0.1:41235|complete|3", o))
        assertFalse(PageRecovery.probeOk(null, o))
        assertFalse(PageRecovery.probeOk("", o))
    }

    @Test fun backoff() {
        assertEquals(listOf(1_000L, 2_000L, 4_000L, 8_000L, 8_000L, 8_000L), (1..6).map { PageRecovery.backoffMs(it) })
    }
}
