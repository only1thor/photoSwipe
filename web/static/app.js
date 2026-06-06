(function() {
  'use strict';

  const SWIPE_DIST = 80;       // px threshold
  const SWIPE_VELOCITY = 0.4;  // px/ms threshold for "flick"
  const HOLD_MS = 350;         // long-press to zoom

  function currentCard() {
    return document.querySelector('#photo-area .card[data-photo-id]');
  }
  function currentClusterCard() {
    return document.querySelector('#photo-area .card[data-card-kind="cluster"]');
  }
  function clusterIsExpanded(card) {
    return card && card.classList.contains('expanded');
  }
  function isSingleCard() {
    return !!currentCard();
  }

  function decide(action) {
    // On a cluster card, keep/trash/skip have stack-specific semantics
    // handled in handleClusterAction; never POST a per-photo decision.
    const cluster = currentClusterCard();
    if (cluster) { handleClusterAction(action, cluster); return; }
    const id = currentCard()?.dataset.photoId;
    if (!id || typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/decision', {
      target: '#photo-area', swap: 'innerHTML',
      values: { action: action, photo_id: id }
    });
  }

  function undo() {
    if (typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/undo', { target: '#photo-area', swap: 'innerHTML' });
  }

  // Cluster: collapsed stack reacts to keep/trash/skip; expanded grid
  // only honors "undo" (the user uses Apply / Skip cluster / corner-ticks).
  function handleClusterAction(action, card) {
    const form = card.querySelector('form');
    if (!form) return;
    if (!clusterIsExpanded(card)) {
      if (action === 'keep') { card.classList.add('expanded'); return; }
      if (action === 'trash') {
        // No keep[] selected; server trashes the whole set.
        form.requestSubmit();
        return;
      }
      if (action === 'skip') {
        const btn = form.querySelector('button[name="action"][value="skip"]');
        if (btn) btn.click();
        return;
      }
    }
    // Expanded: per-photo buttons are inert. Apply / Skip cluster live in
    // the grid itself.
  }

  // --- keyboard ---
  document.addEventListener('keydown', function(e) {
    if (e.target.matches('input, textarea, select')) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    switch (e.key) {
      case 'ArrowLeft': case 'j': case 'J':
        e.preventDefault(); decide('trash'); break;
      case 'ArrowRight': case 'l': case 'L': case 'k': case 'K':
        e.preventDefault(); decide('keep'); break;
      case 'ArrowUp': case 'i': case 'I':
        e.preventDefault(); decide('skip'); break;
      case 'z': case 'Z':
        e.preventDefault(); undo(); break;
      case ' ':
        toggleZoom(); e.preventDefault(); break;
    }
  });

  // --- buttons (event delegation) ---
  document.addEventListener('click', function(e) {
    const infoBtn = e.target.closest('[data-info-toggle]');
    if (infoBtn) {
      const card = infoBtn.closest('.card');
      if (card) {
        const show = !card.classList.contains('show-info');
        card.classList.toggle('show-info', show);
        // Lazy-trigger htmx meta load the first time the panel opens.
        if (show) {
          const panel = card.querySelector('.card-meta.panel');
          if (panel && typeof htmx !== 'undefined') {
            htmx.trigger(panel, 'info-show');
          }
        }
      }
      return;
    }
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'undo') undo(); else decide(action);
  });

  // --- zoom (tap-and-hold / spacebar) ---
  function toggleZoom() {
    const c = currentCard();
    if (!c) return;
    c.classList.toggle('zoomed');
  }
  let holdTimer = null;
  let holdTarget = null;
  document.addEventListener('pointerdown', function(e) {
    // Long-press on a single photo card: zoom the card.
    // Long-press on a cluster thumbnail (in expanded grid): zoom that thumb.
    const thumb = e.target.closest('.cluster-thumb');
    const card = e.target.closest('.card');
    if (!card) return;
    holdTarget = thumb || card;
    holdTimer = setTimeout(function() {
      holdTarget.classList.toggle('zoomed');
      holdTimer = null;
    }, HOLD_MS);
  });
  document.addEventListener('pointerup', cancelHold);
  document.addEventListener('pointercancel', cancelHold);
  document.addEventListener('pointerleave', cancelHold);
  function cancelHold() {
    if (holdTimer) { clearTimeout(holdTimer); holdTimer = null; }
  }
  // Tap-once on a zoomed thumb unzooms it (so users can exit without
  // having to hold again).
  document.addEventListener('click', function(e) {
    const z = e.target.closest('.cluster-thumb.zoomed');
    if (z) { z.classList.remove('zoomed'); e.preventDefault(); }
  }, true);

  // --- touch/mouse drag for swipe ---
  let drag = null;
  function startDrag(e) {
    const card = e.target.closest('.card');
    if (!card || card.classList.contains('zoomed')) return;
    // No swipe gestures inside an expanded cluster grid.
    if (card.dataset.cardKind === 'cluster' && card.classList.contains('expanded')) return;
    const point = e.touches ? e.touches[0] : e;
    drag = {
      card: card,
      x0: point.clientX, y0: point.clientY,
      t0: performance.now()
    };
  }
  function moveDrag(e) {
    if (!drag) return;
    const point = e.touches ? e.touches[0] : e;
    const dx = point.clientX - drag.x0;
    const dy = point.clientY - drag.y0;
    drag.card.style.transform = `translate(${dx}px, ${dy}px) rotate(${dx * 0.04}deg)`;
    drag.card.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
    const absX = Math.abs(dx), absY = Math.abs(dy);
    if (absX > absY) {
      if (dx > 30) drag.card.classList.add('swipe-keep');
      else if (dx < -30) drag.card.classList.add('swipe-trash');
    } else if (dy < -30) {
      drag.card.classList.add('swipe-skip');
    }
  }
  function endDrag(e) {
    if (!drag) return;
    const point = e.changedTouches ? e.changedTouches[0] : e;
    const dx = point.clientX - drag.x0;
    const dy = point.clientY - drag.y0;
    const dt = performance.now() - drag.t0;
    const absX = Math.abs(dx), absY = Math.abs(dy);
    const vel = Math.max(absX, absY) / Math.max(dt, 1);

    let action = null;
    if (absX > absY) {
      if (dx > SWIPE_DIST || (dx > 30 && vel > SWIPE_VELOCITY)) action = 'keep';
      else if (dx < -SWIPE_DIST || (dx < -30 && vel > SWIPE_VELOCITY)) action = 'trash';
    } else {
      if (dy < -SWIPE_DIST || (dy < -30 && vel > SWIPE_VELOCITY)) action = 'skip';
    }

    const c = drag.card;
    const isCluster = c.dataset.cardKind === 'cluster';
    if (action) {
      // For cluster "keep", we expand in place — no fly-off animation.
      if (isCluster && action === 'keep') {
        c.style.transition = 'transform 0.15s ease-out';
        c.style.transform = '';
        c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
        c.classList.add('expanded');
        drag = null;
        return;
      }
      c.style.transition = 'transform 0.18s ease-out, opacity 0.18s ease-out';
      const tx = action === 'keep' ? '120vw' : action === 'trash' ? '-120vw' : '0';
      const ty = action === 'skip' ? '-100vh' : '0';
      c.style.transform = `translate(${tx}, ${ty})`;
      c.style.opacity = 0;
      decide(action);
    } else {
      c.style.transition = 'transform 0.18s ease-out';
      c.style.transform = '';
      c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
    }
    drag = null;
  }

  document.addEventListener('touchstart', startDrag, { passive: true });
  document.addEventListener('touchmove', moveDrag, { passive: true });
  document.addEventListener('touchend', endDrag, { passive: true });

  document.addEventListener('mousedown', function(e) {
    if (e.button !== 0) return;
    if (!e.target.closest('#photo-area')) return;
    // Don't start a drag on form controls or the info button.
    if (e.target.closest('input, button, label')) return;
    startDrag(e);
  });
  document.addEventListener('mousemove', function(e) { if (drag) moveDrag(e); });
  document.addEventListener('mouseup', function(e) { if (drag) endDrag(e); });

  // Tap-to-expand on the collapsed stack (separate from the long-press
  // zoom: a quick tap = expand, a sustained press = zoom the front photo).
  document.addEventListener('click', function(e) {
    const cluster = e.target.closest('.card.cluster');
    if (!cluster || cluster.classList.contains('expanded')) return;
    // Ignore if the user was just dragging (handled in endDrag).
    if (e.target.closest('input, button, label, .stack-count, .stack-hint')) return;
    if (!e.target.closest('.stack-face')) return;
    cluster.classList.add('expanded');
  });
})();
