import type {ReactNode} from 'react';
import clsx from 'clsx';
import Link from '@docusaurus/Link';
import useDocusaurusContext from '@docusaurus/useDocusaurusContext';
import Layout from '@theme/Layout';
import Heading from '@theme/Heading';

import styles from './index.module.css';

function StudyDisclaimer() {
  return (
    <div
      style={{
        background: '#fef3c7',
        color: '#78350f',
        textAlign: 'center',
        padding: '0.6rem 1rem',
        fontSize: '0.9rem',
        borderBottom: '1px solid #f59e0b',
      }}>
      ⚠️ <strong>Study project</strong> — an educational DDD exercise. Not a
      production system.
    </div>
  );
}

function HomepageHeader() {
  const {siteConfig} = useDocusaurusContext();
  return (
    <header className={clsx('hero', styles.heroBanner)}>
      <StudyDisclaimer />
      <div className="container">
        <p className={styles.eyebrow}>
          warehouse-systems · WMS tier · Supporting subdomain
        </p>
        <Heading as="h1" className={styles.heroTitle}>
          {siteConfig.title}
        </Heading>
        <p className={styles.heroSubtitle}>{siteConfig.tagline}</p>
        <p className={styles.heroLead}>
          One aggregate, the SlotPlan, holds a proposal computed by the
          abc-velocity-v1 policy from event-fed copies of demand, product facts
          and the slot layout. A planner approves or rejects it, and an
          approved plan is published as a CloudEvent with the full forward
          assignment map and the moves it implies.
        </p>
        <div className={styles.buttons}>
          <Link className="button button--primary button--lg" to="/docs/overview/introduction">
            Read the docs
          </Link>
          <Link
            className="button button--secondary button--lg"
            to="/docs/api-reference">
            API Reference
          </Link>
          <Link
            className="button button--secondary button--lg"
            to="/docs/adr">
            ADRs
          </Link>
        </div>
      </div>
    </header>
  );
}

export default function Home(): ReactNode {
  const {siteConfig} = useDocusaurusContext();
  return (
    <Layout
      title={siteConfig.title}
      description="Documentation for the Slotting Optimization bounded context: slot plans, the abc-velocity-v1 policy, human approval and the slotting-optimization event stream.">
      <HomepageHeader />
      <main>
        <section className={styles.invariant}>
          <div className="container">
            <blockquote className={styles.invariantQuote}>
              A plan changes nothing until a human approves it, and a site has{' '}
              <strong>at most one Approved plan</strong>: approving a new one
              supersedes the previous one in the same transaction.
            </blockquote>
            <p className={styles.invariantCaption}>
              <Link to="/docs/ddd/aggregate-design-canvas">
                The SlotPlan aggregate →
              </Link>
            </p>
          </div>
        </section>
      </main>
    </Layout>
  );
}
