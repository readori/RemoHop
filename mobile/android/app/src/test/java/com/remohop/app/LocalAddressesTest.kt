package com.remohop.app

import org.junit.Assert.assertEquals
import org.junit.Test
import java.net.InetAddress

class LocalAddressesTest {
    @Test fun formatsForTheCore() {
        val a = listOf(InetAddress.getByName("192.168.1.20"), InetAddress.getByName("fe80::1%1"), InetAddress.getByName("2001:db8::7"))
        assertEquals("192.168.1.20, fe80:0:0:0:0:0:0:1, 2001:db8:0:0:0:0:0:7", LocalAddresses.format(a))
    }
}
