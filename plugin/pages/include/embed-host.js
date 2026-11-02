/*
 * Host side of the embedded Backups view (#526), loaded by Backups.page.
 *
 * The frame sizes to its content, so nothing inside it can tell which part the
 * user can see. The SPA reports its height; this script sizes the frame and
 * reports back the visible part, which keeps dialogs and toasts on screen. Only
 * well-formed messages from this frame, on this origin, are honoured.
 */
(function (root) {
    'use strict';

    var CHILD_SOURCE = 'vault-embed';
    var HOST_SOURCE = 'vault-embed-host';
    var MIN_HEIGHT = 480;
    var PAGE_GAP = 24;
    // Room for Unraid's floating scroll arrow at the bottom of the viewport.
    var ARROW_CLEARANCE = 72;
    var MAX_LENGTH = 1000000;
    var RELOAD_AFTER_MS = 10 * 60 * 1000;
    // Unraid chrome that can stay pinned over the page while it scrolls.
    var CHROME_SELECTORS = ['#header', '#menu', '#footer'];

    function isLength(value) {
        return typeof value === 'number' && isFinite(value) && value >= 0 && value <= MAX_LENGTH;
    }

    function parseChildMessage(event, win, frame) {
        if (event.origin !== win.location.origin || event.source !== frame.contentWindow) return null;
        var data = event.data;
        if (!data || typeof data !== 'object' || data.source !== CHILD_SOURCE) return null;
        if (data.type === 'size' && isLength(data.height)) return { type: 'size', value: data.height };
        if (data.type === 'scroll' && isLength(data.top)) return { type: 'scroll', value: data.top };
        return null;
    }

    function pinnedChrome(win) {
        var doc = win.document;
        var insets = { top: 0, bottom: 0 };
        CHROME_SELECTORS.forEach(function (selector) {
            var el = doc.querySelector(selector);
            if (!el) return;
            var position = win.getComputedStyle(el).position;
            if (position !== 'fixed' && position !== 'sticky') return;
            var rect = el.getBoundingClientRect();
            // A sidebar theme pins a full-height menu to the side; it covers nothing here.
            if (rect.height > win.innerHeight * 0.4) return;
            if (rect.top <= 0) insets.top = Math.max(insets.top, rect.bottom);
            if (rect.bottom >= win.innerHeight) insets.bottom = Math.max(insets.bottom, win.innerHeight - rect.top);
        });
        return insets;
    }

    function attach(frame) {
        var win = frame.ownerDocument.defaultView;
        var contentHeight = 0;
        var lastViewport = '';
        var scheduled = false;
        var hiddenSince = 0;

        // Filling the viewport when the content is short leaves dialogs room to open.
        function floorHeight() {
            var top = frame.getBoundingClientRect().top + win.pageYOffset;
            return Math.max(MIN_HEIGHT, win.innerHeight - top - PAGE_GAP);
        }

        function applyHeight() {
            frame.style.height = Math.ceil(Math.max(floorHeight(), contentHeight)) + 'px';
        }

        function publishViewport() {
            scheduled = false;
            var target = frame.contentWindow;
            if (!target) return;
            var rect = frame.getBoundingClientRect();
            var insets = pinnedChrome(win);
            var visibleTop = Math.max(rect.top, insets.top);
            var visibleBottom = Math.min(rect.bottom, win.innerHeight - Math.max(insets.bottom, ARROW_CLEARANCE));
            var top = Math.max(0, visibleTop - rect.top);
            var height = Math.max(0, visibleBottom - visibleTop);
            var key = top + ':' + height;
            if (key === lastViewport) return;
            lastViewport = key;
            target.postMessage({ source: HOST_SOURCE, type: 'viewport', top: top, height: height }, win.location.origin);
        }

        function scheduleViewport() {
            if (scheduled) return;
            scheduled = true;
            win.requestAnimationFrame(publishViewport);
        }

        function scrollFrameTo(top) {
            var rect = frame.getBoundingClientRect();
            win.scrollTo(0, Math.max(0, win.pageYOffset + rect.top + top - pinnedChrome(win).top));
        }

        function onMessage(event) {
            var message = parseChildMessage(event, win, frame);
            if (!message) return;
            if (message.type === 'size') {
                contentHeight = message.value;
                applyHeight();
                publishViewport();
            } else {
                scrollFrameTo(message.value);
            }
        }

        function onResize() {
            applyHeight();
            scheduleViewport();
        }

        // A new document in the frame knows nothing yet, so always tell it.
        function onLoad() {
            lastViewport = '';
            publishViewport();
        }

        // app.php injects Unraid's csrf_token once per render and the SPA has no
        // in-app refresh. A tab left open across a rotation would have its write
        // actions fail, so reload after a long absence to get a fresh token.
        function onVisibilityChange() {
            var doc = frame.ownerDocument;
            if (doc.hidden) {
                hiddenSince = Date.now();
                return;
            }
            if (hiddenSince && Date.now() - hiddenSince > RELOAD_AFTER_MS) reloadFrame();
            hiddenSince = 0;
        }

        // Assigning the same hash URL to src would not re-render app.php.
        function reloadFrame() {
            try {
                frame.contentWindow.location.reload();
            } catch (e) {
                var src = frame.getAttribute('src');
                frame.src = 'about:blank';
                win.setTimeout(function () { frame.src = src; }, 50);
            }
        }

        win.addEventListener('message', onMessage);
        win.addEventListener('resize', onResize);
        win.addEventListener('scroll', scheduleViewport, { passive: true, capture: true });
        frame.addEventListener('load', onLoad);
        frame.ownerDocument.addEventListener('visibilitychange', onVisibilityChange);
        applyHeight();
        publishViewport();

        return function detach() {
            win.removeEventListener('message', onMessage);
            win.removeEventListener('resize', onResize);
            win.removeEventListener('scroll', scheduleViewport, { capture: true });
            frame.removeEventListener('load', onLoad);
            frame.ownerDocument.removeEventListener('visibilitychange', onVisibilityChange);
        };
    }

    root.VaultEmbedHost = { attach: attach };
})(window);
