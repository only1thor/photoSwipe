(function() {
  'use strict';

  const SWIPE_DIST = 80;       // px threshold
  const SWIPE_VELOCITY = 0.4;  // px/ms threshold for "flick"
  const TAP_SLOP = 10;         // px movement still counts as a tap

  function currentCard() {
    return document.querySelector('#photo-area .card[data-photo-id]');
  }
  function currentClusterCard() {
    return document.querySelector('#photo-area .card[data-card-kind="cluster"]');
  }
  function clusterIsExpanded(card) {
    return card && card.classList.contains('expanded');
  }
  function lightbox() { return document.getElementById('lightbox'); }
  function lightboxOpen() {
    const lb = lightbox();
    return lb && !lb.hasAttribute('hidden');
  }

  function decide(action) {
    if (lightboxOpen()) return; // ignore decisions while zoomed
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
    if (lightboxOpen()) { closeLightbox(); return; }
    if (typeof htmx === 'undefined') return;
    htmx.ajax('POST', '/undo', { target: '#photo-area', swap: 'innerHTML' });
  }

  function handleClusterAction(action, card) {
    const form = card.querySelector('form');
    if (!form) return;
    if (!clusterIsExpanded(card)) {
      if (action === 'keep') { card.classList.add('expanded'); return; }
      if (action === 'trash') { form.requestSubmit(); return; }
      if (action === 'skip') {
        const btn = form.querySelector('button[name="action"][value="skip"]');
        if (btn) btn.click();
        return;
      }
    }
  }

  // --- lightbox: body-level fullscreen zoom -----------------------------
  // Uses a single <div id="lightbox"> in the layout. The image src is set
  // to /photo/{id} (full-quality, browser-cached); pointer-events: none on
  // the img means a tap anywhere bubbles to the overlay's dismiss handler.

  let lightboxZoom = 1;
  function openLightbox(photoId) {
    const lb = lightbox();
    if (!lb || !photoId) return;
    const img = document.getElementById('lightbox-img');
    img.src = '/photo/' + encodeURIComponent(photoId);
    lightboxZoom = 1;
    lb.style.setProperty('--lightbox-zoom', '1');
    lb.removeAttribute('hidden');
    // Block the page behind from scrolling on iOS while zoomed.
    document.documentElement.style.overflow = 'hidden';
  }
  function closeLightbox() {
    const lb = lightbox();
    if (!lb) return;
    lb.setAttribute('hidden', '');
    const img = document.getElementById('lightbox-img');
    if (img) img.src = '';
    document.documentElement.style.overflow = '';
  }
  function adjustLightboxZoom(deltaY, originX, originY) {
    const lb = lightbox();
    if (!lb) return;
    const factor = deltaY > 0 ? 0.88 : 1.12;
    lightboxZoom = Math.max(0.5, Math.min(8, lightboxZoom * factor));
    // Pan origin towards the cursor for "scroll where I'm pointing".
    if (originX != null && originY != null) {
      lb.style.setProperty('transform-origin', `${originX}px ${originY}px`);
    }
    lb.style.setProperty('--lightbox-zoom', String(lightboxZoom));
  }

  // Tap anywhere on the lightbox dismisses it. The img is pointer-events:
  // none so its taps bubble here too.
  document.addEventListener('click', function(e) {
    const lb = lightbox();
    if (!lb || lb.hasAttribute('hidden')) return;
    if (!e.target.closest('#lightbox')) return;
    closeLightbox();
    e.preventDefault();
    e.stopPropagation();
  }, true);

  // Scroll-to-zoom on desktop (any scroll wheel inside the lightbox).
  document.addEventListener('wheel', function(e) {
    if (!lightboxOpen()) return;
    if (!e.target.closest('#lightbox')) return;
    e.preventDefault();
    adjustLightboxZoom(e.deltaY, e.clientX, e.clientY);
  }, { passive: false });

  // --- keyboard ---
  document.addEventListener('keydown', function(e) {
    if (e.target.matches('input, textarea, select')) return;
    if (e.metaKey || e.ctrlKey || e.altKey) return;
    if (e.key === 'Escape') { if (lightboxOpen()) closeLightbox(); return; }
    if (lightboxOpen()) {
      // While zoomed, swallow gesture keys; spacebar/Enter dismisses.
      if (e.key === ' ' || e.key === 'Enter') { e.preventDefault(); closeLightbox(); }
      return;
    }
    switch (e.key) {
      case 'ArrowLeft': case 'j': case 'J':
        e.preventDefault(); decide('trash'); break;
      case 'ArrowRight': case 'l': case 'L': case 'k': case 'K':
        e.preventDefault(); decide('keep'); break;
      case 'ArrowUp': case 'i': case 'I':
        e.preventDefault(); decide('skip'); break;
      case 'z': case 'Z':
        e.preventDefault(); undo(); break;
      case ' ': {
        e.preventDefault();
        const c = currentCard();
        if (c) openLightbox(c.dataset.photoId);
        break;
      }
    }
  });

  // --- buttons + info-toggle ---
  document.addEventListener('click', function(e) {
    if (lightboxOpen()) return; // handled by capture-phase above
    const infoBtn = e.target.closest('[data-info-toggle]');
    if (infoBtn) {
      const card = infoBtn.closest('.card');
      if (card) {
        const show = !card.classList.contains('show-info');
        card.classList.toggle('show-info', show);
        if (show) {
          const panel = card.querySelector('.card-meta.panel');
          if (panel && typeof htmx !== 'undefined') htmx.trigger(panel, 'info-show');
        }
      }
      return;
    }
    const btn = e.target.closest('[data-action]');
    if (!btn) return;
    const action = btn.dataset.action;
    if (action === 'undo') undo(); else decide(action);
  });

  // --- tap-to-zoom on cluster thumbs (the corner ✓ is a separate label) ---
  document.addEventListener('click', function(e) {
    if (lightboxOpen()) return;
    const thumb = e.target.closest('.cluster-thumb');
    if (!thumb) return;
    // Clicks on the corner check toggle the input natively; don't zoom.
    if (e.target.closest('.check-corner')) return;
    const id = thumb.dataset.photoId;
    if (id) openLightbox(id);
  });

  // --- swipe / tap on cards -----------------------------------------------
  let drag = null;
  function startDrag(e) {
    if (lightboxOpen()) return;
    if (e.target.closest('input, button, label, [data-info-toggle], .check-corner')) return;
    const card = e.target.closest('.card');
    if (!card) return;
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
    if (Math.abs(dx) < 4 && Math.abs(dy) < 4) return;
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
    const c = drag.card;
    const isCluster = c.dataset.cardKind === 'cluster';

    let action = null;
    if (absX > absY) {
      if (dx > SWIPE_DIST || (dx > 30 && vel > SWIPE_VELOCITY)) action = 'keep';
      else if (dx < -SWIPE_DIST || (dx < -30 && vel > SWIPE_VELOCITY)) action = 'trash';
    } else {
      if (dy < -SWIPE_DIST || (dy < -30 && vel > SWIPE_VELOCITY)) action = 'skip';
    }

    // Pure tap → zoom (single card) or expand (collapsed cluster).
    if (!action && absX < TAP_SLOP && absY < TAP_SLOP) {
      c.style.transition = '';
      c.style.transform = '';
      c.classList.remove('swipe-keep', 'swipe-trash', 'swipe-skip');
      if (isCluster && !c.classList.contains('expanded')) {
        c.classList.add('expanded');
      } else if (!isCluster) {
        openLightbox(c.dataset.photoId);
      }
      drag = null;
      return;
    }

    if (action) {
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
    startDrag(e);
  });
  document.addEventListener('mousemove', function(e) { if (drag) moveDrag(e); });
  document.addEventListener('mouseup', function(e) { if (drag) endDrag(e); });
})();
