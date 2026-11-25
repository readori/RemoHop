package com.remohop.app

import android.content.DialogInterface
import android.widget.EditText
import androidx.test.core.app.ActivityScenario
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * On a device or emulator: each kind of native dialog the page can ask for
 * opens and answers. The prompt once crashed as it opened (its text field
 * was added to the TextInputLayout with the wrong LayoutParams), which the
 * JVM tests of the request parsing could not see.
 */
@RunWith(AndroidJUnit4::class)
class NativeDialogTest {
    private fun request(kind: String, actions: List<DialogRequest.Action> = emptyList()) = DialogRequest(
        kind = kind, title = "Rename", message = "", confirm = "OK", cancel = "Cancel", destructive = kind == "confirm",
        value = "old name.txt", placeholder = "Name", actions = actions,
    )

    @Test fun promptOpensAndReturnsTheText() {
        ActivityScenario.launch(MainActivity::class.java).use { s ->
            var answer: DialogAnswer? = null
            s.onActivity { a ->
                val d = NativeDialog.show(a, request("prompt")) { answer = it }
                assertTrue("prompt is showing", d.isShowing)
                val field = findEditText(d.window!!.decorView)
                assertNotNull("prompt has a text field", field)
                assertEquals("old name.txt", field!!.text.toString())
                field.setText("new name.txt")
                d.getButton(DialogInterface.BUTTON_POSITIVE).performClick()
            }
            assertEquals(DialogAnswer.Confirmed("new name.txt"), answer)
        }
    }

    @Test fun confirmAndActionsOpenAndAnswerOnce() {
        ActivityScenario.launch(MainActivity::class.java).use { s ->
            val answers = mutableListOf<DialogAnswer>()
            s.onActivity { a ->
                val d = NativeDialog.show(a, request("confirm")) { answers += it }
                assertTrue(d.isShowing)
                d.getButton(DialogInterface.BUTTON_NEGATIVE).performClick()
                val acts = NativeDialog.show(a, request("actions", listOf(DialogRequest.Action("Rename", false), DialogRequest.Action("Delete", true)))) { answers += it }
                assertTrue(acts.isShowing)
                acts.listView.performItemClick(acts.listView, 1, 1)
            }
            assertEquals(listOf(DialogAnswer.Cancelled, DialogAnswer.Picked(1)), answers)
        }
    }

    private fun findEditText(v: android.view.View): EditText? {
        if (v is EditText) return v
        if (v is android.view.ViewGroup) for (i in 0 until v.childCount) findEditText(v.getChildAt(i))?.let { return it }
        return null
    }
}
