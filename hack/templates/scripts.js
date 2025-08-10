/**
 * Copyright 2025 Hedgehog
 * SPDX-License-Identifier: Apache-2.0
 */

let currentDrawIODiagram = null;
let currentGraphvizDiagram = null;
let currentMermaidDiagram = null;
let currentTopology = null;
let currentTheme = 'light';
let lazyLoadObserver = null;

document.addEventListener('DOMContentLoaded', function() {
    initializeTheme();
    initializeLazyLoading();
    initializeDiagrams();
    setupEventListeners();
});

function initializeTheme() {
    const savedTheme = localStorage.getItem('hedgehog-diagram-theme') || 'light';
    setTheme(savedTheme);

    const themeToggle = document.getElementById('theme-toggle');
    if (themeToggle) {
        themeToggle.addEventListener('click', toggleTheme);
    }
}

function setTheme(theme) {
    currentTheme = theme;
    document.documentElement.setAttribute('data-theme', theme);

    const isDark = theme === 'dark';
    const buttonText = isDark ? '☀️ Light' : '🌙 Dark';

    const themeToggle = document.getElementById('theme-toggle');
    if (themeToggle) themeToggle.textContent = buttonText;

    setTimeout(() => {
        applyThemeToSVGs(theme);
    }, 100);

    try {
        localStorage.setItem('hedgehog-diagram-theme', theme);
    } catch (e) {
        // Ignore localStorage errors
    }
}

function toggleTheme() {
    const newTheme = currentTheme === 'light' ? 'dark' : 'light';
    setTheme(newTheme);
}

function applyThemeToSVGs(theme) {
    const svgElements = document.querySelectorAll('.svg-diagram');
    svgElements.forEach(svg => {
        if (theme === 'dark') {
            svg.setAttribute('data-dark-mode', 'true');
        } else {
            svg.removeAttribute('data-dark-mode');
        }
    });
}

function initializeLazyLoading() {
    if ('IntersectionObserver' in window) {
        lazyLoadObserver = new IntersectionObserver(function(entries) {
            entries.forEach(function(entry) {
                if (entry.isIntersecting) {
                    loadDiagram(entry.target);
                    lazyLoadObserver.unobserve(entry.target);
                }
            });
        }, {
            rootMargin: '50px'
        });

        document.querySelectorAll('.lazy-placeholder').forEach(function(placeholder) {
            lazyLoadObserver.observe(placeholder);
        });
    } else {
        document.querySelectorAll('.lazy-placeholder').forEach(loadDiagram);
    }
}

function loadDiagram(placeholder) {
    const src = placeholder.getAttribute('data-src');
    const alt = placeholder.getAttribute('data-alt');

    if (!src) return;

    let element;
    if (src.includes('mermaid') || src.includes('drawio')) {
        element = document.createElement('object');
        element.data = src;
        element.type = 'image/svg+xml';
        element.className = 'svg-diagram';

        const fallback = document.createElement('img');
        fallback.src = src;
        fallback.alt = alt;
        element.appendChild(fallback);

        element.onload = function() {
            setTimeout(() => {
                applyThemeToSVGs(currentTheme);
            }, 100);
        };

        element.onerror = function() {
            console.warn('Failed to load SVG object:', src);
            const imgElement = document.createElement('img');
            imgElement.src = src;
            imgElement.alt = alt;
            imgElement.className = 'svg-diagram';
            imgElement.style.opacity = '0';
            imgElement.style.transition = 'opacity 0.3s ease';
            imgElement.onload = function() {
                this.style.opacity = '1';
                applyThemeToSVGs(currentTheme);
            };
            placeholder.parentNode.replaceChild(imgElement, element);
        };
    } else {
        element = document.createElement('img');
        element.src = src;
        element.alt = alt;
        element.className = 'svg-diagram';
        element.style.opacity = '0';
        element.style.transition = 'opacity 0.3s ease';
        element.onload = function() {
            this.style.opacity = '1';
            applyThemeToSVGs(currentTheme);
        };
        element.onerror = function() {
            console.warn('Failed to load SVG image:', src);
            this.style.display = 'none';
            const errorDiv = document.createElement('div');
            errorDiv.className = 'no-content';
            errorDiv.textContent = 'Failed to load diagram: ' + alt;
            this.parentElement.appendChild(errorDiv);
        };
    }

    placeholder.parentNode.replaceChild(element, placeholder);

    if (src.includes('mermaid')) {
        addMermaidZoom(element);
    }
}

function addMermaidZoom(element) {
    if (element.hasAttribute('data-zoom-handler')) return;

    element.setAttribute('data-zoom-handler', 'true');
    element.style.cursor = 'zoom-in';
    element.onclick = function() {
        if (this.style.maxWidth === 'none') {
            this.style.maxWidth = '95%';
            this.style.maxHeight = '80vh';
            this.style.cursor = 'zoom-in';
            this.parentElement.style.overflow = 'auto';
        } else {
            this.style.maxWidth = 'none';
            this.style.maxHeight = 'none';
            this.style.cursor = 'zoom-out';
            this.parentElement.style.overflow = 'auto';
        }
    };
}

function initializeDiagrams() {
    document.querySelectorAll('.diagram-container').forEach(container => {
        container.classList.add('hidden');
    });

    setTimeout(function() {
        const activeTab = document.querySelector('.tab.active');
        if (activeTab) {
            if (activeTab.textContent.includes('DrawIO')) {
                const firstLink = document.querySelector('#drawio-toc a');
                if (firstLink) {
                    const match = firstLink.getAttribute('onclick').match(/showDrawIODiagram\('([^']+)'\)/);
                    if (match) showDrawIODiagram(match[1]);
                }
            } else if (activeTab.textContent.includes('Graphviz')) {
                const firstLink = document.querySelector('#graphviz-toc a');
                if (firstLink) {
                    const match = firstLink.getAttribute('onclick').match(/showGraphvizDiagram\('([^']+)'\)/);
                    if (match) showGraphvizDiagram(match[1]);
                }
            } else if (activeTab.textContent.includes('Mermaid')) {
                const firstLink = document.querySelector('#mermaid-toc a');
                if (firstLink) {
                    const match = firstLink.getAttribute('onclick').match(/showMermaidDiagram\('([^']+)'\)/);
                    if (match) showMermaidDiagram(match[1]);
                }
            }
        }
    }, 100);
}

function setupEventListeners() {
    document.addEventListener('keydown', function(e) {
        if (e.target.tagName === 'INPUT' || e.target.tagName === 'TEXTAREA') return;

        switch(e.key) {
            case 'ArrowLeft':
                navigateDiagram(-1);
                e.preventDefault();
                break;
            case 'ArrowRight':
                navigateDiagram(1);
                e.preventDefault();
                break;
        }
    });
}

function navigateDiagram(direction) {
    const activeTab = document.querySelector('.tab.active');
    if (!activeTab) return;

    let currentIndex = -1;
    let links = [];

    if (activeTab.textContent.includes('DrawIO')) {
        links = Array.from(document.querySelectorAll('#drawio-toc a'));
        const activeLink = document.querySelector('#drawio-toc a.active');
        if (activeLink) {
            currentIndex = links.indexOf(activeLink);
        } else {
            const currentName = currentDrawIODiagram;
            currentIndex = links.findIndex(link =>
                link.getAttribute('onclick').includes(`'${currentName}'`)
            );
        }
    } else if (activeTab.textContent.includes('Graphviz')) {
        links = Array.from(document.querySelectorAll('#graphviz-toc a'));
        const activeLink = document.querySelector('#graphviz-toc a.active');
        if (activeLink) {
            currentIndex = links.indexOf(activeLink);
        } else {
            const currentName = currentGraphvizDiagram;
            currentIndex = links.findIndex(link =>
                link.getAttribute('onclick').includes(`'${currentName}'`)
            );
        }
    } else if (activeTab.textContent.includes('Mermaid')) {
        links = Array.from(document.querySelectorAll('#mermaid-toc a'));
        const activeLink = document.querySelector('#mermaid-toc a.active');
        if (activeLink) {
            currentIndex = links.indexOf(activeLink);
        } else {
            const currentName = currentMermaidDiagram;
            currentIndex = links.findIndex(link =>
                link.getAttribute('onclick').includes(`'${currentName}'`)
            );
        }
    }

    if (links.length === 0) return;

    const nextIndex = (currentIndex + direction + links.length) % links.length;
    links[nextIndex].click();
}

function showTab(tabName) {
    document.querySelectorAll(".tab-content").forEach(content => content.classList.remove("active"));
    document.querySelectorAll(".tab").forEach(tab => tab.classList.remove("active"));
    document.getElementById(tabName + "-content").classList.add("active");
    event.target.classList.add("active");

    document.querySelectorAll(".sidebar .toc").forEach(toc => {
        toc.classList.remove("active");
        toc.style.display = "none";
    });

    const activeToc = document.getElementById(tabName + "-toc");
    if (activeToc) {
        activeToc.classList.add("active");
        activeToc.style.display = "block";
    }

    let diagramToShow = currentTopology;

    if (tabName === 'drawio') {
        if (diagramToShow) {
            const targetLink = document.querySelector('#drawio-toc a[onclick*="' + diagramToShow + '"]');
            if (targetLink) {
                showDrawIODiagram(diagramToShow);
                return;
            }
        }
        const firstLink = document.querySelector('#drawio-toc a');
        if (firstLink) {
            const match = firstLink.getAttribute('onclick').match(/showDrawIODiagram\('([^']+)'\)/);
            if (match) showDrawIODiagram(match[1]);
        }
    } else if (tabName === 'graphviz') {
        if (diagramToShow) {
            const targetLink = document.querySelector('#graphviz-toc a[onclick*="' + diagramToShow + '"]');
            if (targetLink) {
                showGraphvizDiagram(diagramToShow);
                return;
            }
        }
        const firstLink = document.querySelector('#graphviz-toc a');
        if (firstLink) {
            const match = firstLink.getAttribute('onclick').match(/showGraphvizDiagram\('([^']+)'\)/);
            if (match) showGraphvizDiagram(match[1]);
        }
    } else if (tabName === 'mermaid') {
        if (diagramToShow) {
            const targetLink = document.querySelector('#mermaid-toc a[onclick*="' + diagramToShow + '"]');
            if (targetLink) {
                showMermaidDiagram(diagramToShow);
                return;
            }
        }
        const firstLink = document.querySelector('#mermaid-toc a');
        if (firstLink) {
            const match = firstLink.getAttribute('onclick').match(/showMermaidDiagram\('([^']+)'\)/);
            if (match) showMermaidDiagram(match[1]);
        }
    }

    setTimeout(() => applyThemeToSVGs(currentTheme), 100);
}

function showDrawIOStyle(style) {
    document.querySelectorAll(".style-content").forEach(content => content.classList.remove("active"));
    const newStyleContent = document.getElementById("drawio-style-" + style);
    if (newStyleContent) {
        newStyleContent.classList.add("active");
    }

    document.querySelectorAll('.style-selector').forEach(selector => {
        selector.querySelectorAll('.style-btn').forEach(btn => btn.classList.remove('active'));
        const activeBtn = selector.querySelector('[onclick*="' + style + '"]');
        if (activeBtn) activeBtn.classList.add('active');
    });

    if (currentDrawIODiagram && newStyleContent) {
        const diagramContainer = newStyleContent.querySelector('[data-diagram="' + currentDrawIODiagram + '"]');
        if (diagramContainer) {
            newStyleContent.querySelectorAll('.diagram-container').forEach(container => {
                container.classList.add('hidden');
            });
            diagramContainer.classList.remove('hidden');
        } else {
            const firstDiagram = newStyleContent.querySelector('.diagram-container:not(.no-content)');
            if (firstDiagram) {
                newStyleContent.querySelectorAll('.diagram-container').forEach(container => {
                    container.classList.add('hidden');
                });
                firstDiagram.classList.remove('hidden');
                currentDrawIODiagram = firstDiagram.getAttribute('data-diagram');
                currentTopology = currentDrawIODiagram;
            }
        }
    } else if (newStyleContent) {
        const firstDiagram = newStyleContent.querySelector('.diagram-container:not(.no-content)');
        if (firstDiagram) {
            newStyleContent.querySelectorAll('.diagram-container').forEach(container => {
                container.classList.add('hidden');
            });
            firstDiagram.classList.remove('hidden');
            currentDrawIODiagram = firstDiagram.getAttribute('data-diagram');
            currentTopology = currentDrawIODiagram;

            document.querySelectorAll('#drawio-toc a').forEach(a => a.classList.remove('active'));
            const tocLink = document.querySelector('#drawio-toc a[onclick*="' + currentDrawIODiagram + '"]');
            if (tocLink) tocLink.classList.add('active');
        }
    }

    setTimeout(() => applyThemeToSVGs(currentTheme), 100);
}

function showDrawIODiagram(diagramName) {
    const activeStyle = document.querySelector('.style-content.active');
    if (activeStyle) {
        activeStyle.querySelectorAll('.diagram-container').forEach(container => {
            container.classList.add('hidden');
        });

        const diagramContainer = activeStyle.querySelector('[data-diagram="' + diagramName + '"]');
        if (diagramContainer) {
            diagramContainer.classList.remove('hidden');
            currentDrawIODiagram = diagramName;
            currentTopology = diagramName;

            document.querySelectorAll('#drawio-toc a').forEach(a => a.classList.remove('active'));
            const tocLink = document.querySelector('#drawio-toc a[onclick*="' + diagramName + '"]');
            if (tocLink) tocLink.classList.add('active');

            window.scrollTo({ top: 0, behavior: 'smooth' });

            setTimeout(() => applyThemeToSVGs(currentTheme), 100);
        }
    }
}

function showGraphvizDiagram(diagramName) {
    document.querySelectorAll('#graphviz-content .diagram-container').forEach(container => {
        container.classList.add('hidden');
    });

    const diagramContainer = document.querySelector('#graphviz-content [data-diagram="' + diagramName + '"]');
    if (diagramContainer) {
        diagramContainer.classList.remove('hidden');
        currentGraphvizDiagram = diagramName;
        currentTopology = diagramName;

        document.querySelectorAll('#graphviz-toc a').forEach(a => a.classList.remove('active'));
        const tocLink = document.querySelector('#graphviz-toc a[onclick*="' + diagramName + '"]');
        if (tocLink) tocLink.classList.add('active');

        window.scrollTo({ top: 0, behavior: 'smooth' });

        setTimeout(() => applyThemeToSVGs(currentTheme), 100);
    }
}

function showMermaidDiagram(diagramName) {
    document.querySelectorAll('#mermaid-content .diagram-container').forEach(container => {
        container.classList.add('hidden');
    });

    const diagramContainer = document.querySelector('#mermaid-content [data-diagram="' + diagramName + '"]');
    if (diagramContainer) {
        diagramContainer.classList.remove('hidden');
        currentMermaidDiagram = diagramName;
        currentTopology = diagramName;

        const img = diagramContainer.querySelector('.svg-diagram');
        if (img && !img.hasAttribute('data-zoom-handler')) {
            addMermaidZoom(img);
        }

        document.querySelectorAll('#mermaid-toc a').forEach(a => a.classList.remove('active'));
        const tocLink = document.querySelector('#mermaid-toc a[onclick*="' + diagramName + '"]');
        if (tocLink) tocLink.classList.add('active');

        window.scrollTo({ top: 0, behavior: 'smooth' });

        setTimeout(() => applyThemeToSVGs(currentTheme), 100);
    }
}

function showSVGTooltip(diagramName, content, x, y) {
    const tooltip = document.getElementById('tooltip-' + diagramName);
    if (tooltip) {
        tooltip.textContent = content;
        tooltip.style.left = x + 'px';
        tooltip.style.top = y + 'px';
        tooltip.classList.add('visible');
    }
}

function hideSVGTooltip(diagramName) {
    const tooltip = document.getElementById('tooltip-' + diagramName);
    if (tooltip) {
        tooltip.classList.remove('visible');
    }
}

window.showTooltip = showSVGTooltip;
window.hideTooltip = hideSVGTooltip;
